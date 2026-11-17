// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || google)

package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/IgnatG/infraharvest/internal/cli"
	"github.com/IgnatG/infraharvest/internal/providers/google"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// discoverGoogleWith runs infraharvest discover google with args alone,
// with no scope but what they say, and returns the selection file it
// writes.
func discoverGoogleWith(t *testing.T, e googleEstate, args ...string) *selection.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "selection.yaml")
	root := cli.NewCmdRoot()
	root.SetArgs(append([]string{"discover", "google", "--pick=false", "--out=" + t.TempDir(), "--selection=" + path}, args...))
	if err := root.ExecuteContext(e.ctx); err != nil {
		t.Fatalf("infraharvest discover google %v: %v", args, err)
	}
	f, err := selection.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestGoogleManagedStateGCS checks --managed-state with a gs:// URL, which
// infraharvest reads (as the gcs backend keeps it) from the Cloud Storage
// emulator at STORAGE_EMULATOR_HOST, as Google's own client libraries
// do: the state of the Pub/Sub topic testdata/google creates is put in a
// bucket, as the gcs backend would keep it, and discover leaves the topic
// out as managed by Terraform, and still includes what the state doesn't
// have, the subscription. A state under another prefix is not read.
func TestGoogleManagedStateGCS(t *testing.T) {
	e := seedGoogle(t)
	useAssetSearch(t, e, e.state)
	t.Setenv("STORAGE_EMULATOR_HOST", e.endpoint)

	topic := ""
	for _, r := range e.state {
		if r.Type == "google_pubsub_topic" {
			topic, _ = r.AttributeValues["id"].(string)
		}
	}
	if topic == "" {
		t.Fatal("testdata/google creates no Pub/Sub topic")
	}
	state, err := json.Marshal(map[string]any{
		"version": 4,
		"resources": []map[string]any{{
			"mode": "managed", "type": "google_pubsub_topic", "name": "events",
			"instances": []map[string]any{{"attributes": map[string]any{"id": topic}}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	const bucket = "e2e-managed-state-gcs"
	putGCSObject(t, e.endpoint, "", bucket, "")
	putGCSObject(t, e.endpoint, bucket, "prod/default.tfstate", string(state))
	putGCSObject(t, e.endpoint, bucket, "other/default.tfstate", `{"version":4}`)

	f := discoverGoogleWith(t, e, append(googleScope(), "--managed-state=gs://"+bucket+"/prod")...)
	managed := f.Resources[selectionEntry(t, f, "google_pubsub_topic", "/infraharvest-e2e-events")]
	if managed.Include || !strings.HasPrefix(managed.Reason, report.TerraformPrefix) {
		t.Errorf("the topic the state in gs://%s/prod has: include=%v %q, want it left out as managed by Terraform", bucket, managed.Include, managed.Reason)
	}
	subscription := f.Resources[selectionEntry(t, f, "google_pubsub_subscription", "/infraharvest-e2e-worker")]
	if !subscription.Include {
		t.Errorf("the subscription, which the state doesn't have, is left out: %q", subscription.Reason)
	}

	// The prefix scopes the state read: the other prefix has no resources.
	f = discoverGoogleWith(t, e, append(googleScope(), "--managed-state=gs://"+bucket+"/other")...)
	if r := f.Resources[selectionEntry(t, f, "google_pubsub_topic", "/infraharvest-e2e-events")]; !r.Include {
		t.Errorf("the topic is left out by the state under another prefix: %q", r.Reason)
	}
}

// putGCSObject creates the bucket (with no name) or puts the object name
// with content in it, through the JSON API of the emulator at endpoint.
func putGCSObject(t *testing.T, endpoint, bucket, name, content string) {
	t.Helper()
	target, body, contentType := endpoint+"/upload/storage/v1/b/"+bucket+"/o?uploadType=media&name="+url.QueryEscape(name), content, "application/json"
	if bucket == "" {
		// name is the bucket to create.
		target, body = endpoint+"/storage/v1/b?project="+googleProject, `{"name":"`+name+`"}`
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create gs://%s/%s in the emulator: %s", bucket, name, resp.Status)
	}
}

// resourceManager stands in for the Resource Manager API, which the
// emulator doesn't serve, in front of the endpoint at target, which gets
// every other request: the organization 1234 has two projects, one of them
// deleted, a folder with the project testdata/google creates its resources
// in and a subfolder, and a deleted folder; the folder 444 has the project
// too. It records the parents it was asked for.
type resourceManager struct {
	*httptest.Server
	mu      sync.Mutex
	parents []string
}

func newResourceManager(t *testing.T, target string) *resourceManager {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	type project struct {
		ProjectID string `json:"projectId"`
		State     string `json:"state"`
	}
	type folder struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	projects := map[string][]project{
		"organizations/1234": {{googleProject, "ACTIVE"}, {"e2e-deleted-project", "DELETE_REQUESTED"}},
		"folders/111":        {{googleProject, "ACTIVE"}},
		"folders/444":        {{googleProject, "ACTIVE"}},
	}
	folders := map[string][]folder{
		"organizations/1234": {{"folders/111", "ACTIVE"}, {"folders/222", "DELETE_REQUESTED"}},
		"folders/111":        {{"folders/333", "ACTIVE"}},
	}
	rm := &resourceManager{}
	rm.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := strings.TrimPrefix(r.URL.Path, "/v3/")
		if kind != "projects" && kind != "folders" {
			proxy.ServeHTTP(w, r)
			return
		}
		parent := r.URL.Query().Get("parent")
		w.Header().Set("Content-Type", "application/json")
		if kind == "projects" {
			rm.mu.Lock()
			rm.parents = append(rm.parents, parent)
			rm.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"projects": projects[parent]})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"folders": folders[parent]})
	}))
	t.Cleanup(rm.Close)
	return rm
}

// TestGoogleOrganizationFolders checks --organization and --folders, which
// import every active project under an organization and some folders, in
// their subfolders too, instead of --projects: discover asks for the
// projects of the organization, of each active folder and subfolder under
// it, and of the folder named, lists the project of the estate once
// whichever way it is reached, and not the deleted one; a deleted folder
// isn't read. The emulator doesn't serve Resource Manager's lists, so a
// stand-in in front of it does.
func TestGoogleOrganizationFolders(t *testing.T) {
	e := seedGoogle(t)
	rm := newResourceManager(t, assetSearch(t, e.endpoint, e.state).URL)
	t.Setenv(google.EndpointEnv, rm.URL)

	f := discoverGoogleWith(t, e, "--organization=1234", "--folders=444", "--services=*", "--regions=global,us-central1")
	projects := map[string]bool{}
	for _, r := range f.Resources {
		parts := strings.Split(r.Scope, "/")
		if len(parts) < 2 {
			t.Errorf("%s %s has the scope %q", r.Type, r.ID, r.Scope)
			continue
		}
		projects[parts[1]] = true
	}
	if len(projects) != 1 || !projects[googleProject] {
		t.Errorf("--organization and --folders list the projects %v, want %s alone", projects, googleProject)
	}
	topic := 0
	for _, r := range f.Resources {
		if r.Type == "google_pubsub_topic" {
			topic++
		}
	}
	if topic != 1 {
		t.Errorf("the project's topic is listed %d times, want once", topic)
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()
	for _, parent := range []string{"organizations/1234", "folders/111", "folders/333", "folders/444"} {
		if !slices.Contains(rm.parents, parent) {
			t.Errorf("discover didn't list the projects of %s; it asked for %v", parent, rm.parents)
		}
	}
	if slices.Contains(rm.parents, "folders/222") {
		t.Errorf("discover read the deleted folder 222: %v", rm.parents)
	}
}

// TestGoogleBeta checks --beta, which generates configuration for the
// google-beta provider instead of google: the root of the region
// testdata/google creates a key ring in requires hashicorp/google-beta
// and none of google, creates the key ring and its keys with google-beta,
// and plans as imports with no changes with it.
func TestGoogleBeta(t *testing.T) {
	e := seedGoogle(t)
	useAssetSearch(t, e, e.state)

	out := importGoogleWith(t, e, "--projects="+googleProject, "--services=*", "--regions=us-central1", "--beta")
	dirs := generatedDirs(t, out)
	if len(dirs) != 1 {
		t.Fatalf("--beta: roots %v, want the region's alone", dirs)
	}
	versions := readFile(t, filepath.Join(dirs[0], "versions.tf"))
	if !strings.Contains(versions, `"hashicorp/google-beta"`) || strings.Contains(versions, `"hashicorp/google"`) {
		t.Errorf("--beta: versions.tf doesn't require google-beta alone:\n%s", versions)
	}
	generated := configuration(t, dirs[0])
	if !strings.Contains(generated, "google_kms_key_ring") || !strings.Contains(generated, "provider = google-beta") || strings.Contains(generated, "provider = google\n") {
		t.Errorf("--beta: the key ring isn't created with google-beta:\n%s", generated)
	}
	if imported := checkNoChanges(e.ctx, t, dirs[0], e.execPath, e.plugins, nil); imported["google_kms_key_ring"] != 1 || imported["google_kms_crypto_key"] == 0 {
		t.Errorf("--beta: the root imports %v, want the key ring and its keys", imported)
	}
}

// importGoogleWith runs infraharvest import google with e's engine and
// args alone into a new directory it returns, failing the test unless
// every resource is imported.
func importGoogleWith(t *testing.T, e googleEstate, args ...string) string {
	t.Helper()
	out := t.TempDir()
	root := cli.NewCmdRoot()
	root.SetArgs(append([]string{
		"import", "google",
		"--engine=" + e.engineName,
		"--terraform-path=" + e.execPath,
		"--out=" + out,
		"--all",
	}, args...))
	if err := root.ExecuteContext(e.ctx); err != nil {
		t.Fatalf("infraharvest import google %v: %v", args, err)
	}
	return out
}
