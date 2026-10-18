// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || google)

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/infraharvest/infraharvest-cli/internal/adapters"
	"github.com/infraharvest/infraharvest-cli/internal/cli"
	"github.com/infraharvest/infraharvest-cli/internal/importer"
	"github.com/infraharvest/infraharvest-cli/internal/providers/google"
	"github.com/infraharvest/infraharvest-cli/internal/report"
)

// googleProject is the project testdata/google creates its resources in.
const googleProject = "infraharvest-e2e"

// TestGoogleRoundTrip creates resources in floci-gcp, a Google Cloud emulator, imports
// them with --engine=terraform (or E2E_ENGINE=tofu) and checks the engine
// plans every one as an import with no changes.
//
// floci-gcp doesn't serve Cloud Asset Inventory, which cloud_asset lists
// through, or Cloud DNS: a stand-in answers the searches from what the
// test created, and from what Google Cloud would create itself, which the default
// selection leaves out, and finds no DNS zones. floci-gcp serves the rest:
// the project's IAM policy, the Cloud SQL and Cloud Scheduler lists, and
// what Terraform plans with.
func TestGoogleRoundTrip(t *testing.T) {
	e := seedGoogle(t)
	ctx, engineName, execPath, plugins, state := e.ctx, e.engineName, e.execPath, e.plugins, e.state

	search := assetSearch(t, e.endpoint, state)
	t.Setenv(google.EndpointEnv, search.URL)

	// Every service: global resources in global's root, with the network's
	// subnetwork, the key ring in us-central1's, and the project's IAM
	// members, which iam lists, in global's with the service account.
	coverage := checkGoogleImport(ctx, t, engineName, execPath, plugins, state, "--services=*", "--regions=global,us-central1")
	// The network, its subnetwork and its firewall rule in one call of the
	// network module, in global's root.
	checkGoogleModules(t, coverage, map[string][]string{
		adapters.GoogleServiceAccount.Source: {"google_service_account", "google_project_iam_member"},
		adapters.GoogleProjectIAM.Source:     {"google_project_iam_member"},
		adapters.GoogleNetwork.Source:        {"google_compute_network", "google_compute_subnetwork", "google_compute_firewall"},
	})
	// The grants to people and the group in one call; the service
	// account's stays with the account.
	if got := moduleResources(coverage, adapters.GoogleProjectIAM.Source); len(got) != 3 {
		t.Errorf("%s: %v, want the 3 grants to people and the group", adapters.GoogleProjectIAM.Source, got)
	}
	for _, id := range []string{"projects/" + googleProject + "/global/networks/default", "projects/" + googleProject + "/global/firewalls/default-allow-ssh"} {
		if !slices.ContainsFunc(coverage.Excluded, func(e report.Excluded) bool { return e.ID == id && e.Reason != "" }) {
			t.Errorf("cloud_asset: %s isn't left out by default: %+v", id, coverage.Excluded)
		}
	}

	// Roots per environment, from the bucket's env label: it in prod's
	// global root, as it is in the US multi-region; the rest, unlabelled,
	// in shared's.
	coverage = checkGoogleImport(ctx, t, engineName, execPath, plugins, state, "--services=*", "--regions=global,us-central1", "--env-tags=env")
	var envRoots []string
	for _, d := range coverage.Directories {
		envRoots = append(envRoots, d.Path)
	}
	slices.Sort(envRoots)
	if want := []string{
		"envs/prod/google/" + googleProject + "/global",
		"envs/shared/google/" + googleProject + "/global",
		"envs/shared/google/" + googleProject + "/us-central1",
	}; !slices.Equal(envRoots, want) {
		t.Errorf("--env-tags=env: roots %v, want %v", envRoots, want)
	}
}

// googleEstate is testdata/google, created in floci-gcp for one test, and
// the engine that test imports it with.
type googleEstate struct {
	ctx                  context.Context
	engineName, execPath string
	plugins              string
	// endpoint is floci-gcp's, for a stand-in in front of it (see
	// assetSearch).
	endpoint string
	state    []*tfjson.StateResource
}

// googleSeeded is what seedGoogle created in this process. floci-gcp
// can't create testdata/google twice, so the tests run against one
// floci-gcp share it; CI runs them on runners of their own (see the
// e2e-google job).
var googleSeeded struct {
	sync.Mutex
	state []*tfjson.StateResource
}

// seedGoogle creates testdata/google in floci-gcp, at the endpoint
// google.EndpointEnv names, with Terraform (or OpenTofu with
// E2E_ENGINE=tofu), unless an earlier test did, skipping the test when
// there is no emulator.
func seedGoogle(t *testing.T) googleEstate {
	t.Helper()
	endpoint := os.Getenv(google.EndpointEnv)
	if endpoint == "" {
		skipUnconfigured(t, google.EndpointEnv+" is not set; start floci-gcp in e2e/compose.yaml")
	}
	if !isLoopback(endpoint) {
		t.Fatalf("%s=%s: the test creates resources, so it only runs against a local emulator", google.EndpointEnv, endpoint)
	}
	isolateGoogleConfig(t, endpoint)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)
	cache := t.TempDir()
	engineName, binary := e2eEngine(t)
	execPath, err := binary.Find(ctx, "", filepath.Join(cache, binary.Name))
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(cache, "plugins")
	googleSeeded.Lock()
	defer googleSeeded.Unlock()
	if googleSeeded.state == nil {
		googleSeeded.state = seed(ctx, t, "google", execPath, plugins)
	}
	return googleEstate{ctx: ctx, engineName: engineName, execPath: execPath, plugins: plugins, endpoint: endpoint, state: googleSeeded.state}
}

// googleScope are the flags that say what the Google Cloud tests list:
// every service, in the project testdata/google creates its resources in,
// in the regions it creates them in.
func googleScope() []string {
	return []string{"--projects=" + googleProject, "--services=*", "--regions=global,us-central1"}
}

// importGoogle runs infraharvest import google with e's engine on what
// googleScope says, and args, into out, or a new directory if out is "",
// and returns the directory.
func importGoogle(t *testing.T, e googleEstate, out string, args ...string) string {
	t.Helper()
	if out == "" {
		out = t.TempDir()
	}
	root := cli.NewCmdRoot()
	root.SetArgs(append(append([]string{
		"import", "google",
		"--engine=" + e.engineName,
		"--terraform-path=" + e.execPath,
		"--out=" + out,
		// Keep going after a failure, so one run reports every problem;
		// the checks on coverage.json still fail the test.
		"--allow-partial",
	}, googleScope()...), args...))
	if err := root.ExecuteContext(e.ctx); importer.ExitCode(err) != report.ExitOK && importer.ExitCode(err) != report.ExitPartial {
		t.Fatalf("infraharvest import google %v: %v", args, err)
	}
	return out
}

// discoverGoogle runs infraharvest discover google on what googleScope
// says into out, where it saves its inventories, and returns the
// selection file it writes.
func discoverGoogle(t *testing.T, e googleEstate, out string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "selection.yaml")
	root := cli.NewCmdRoot()
	root.SetArgs(append([]string{"discover", "google", "--pick=false", "--out=" + out, "--selection=" + path}, googleScope()...))
	if err := root.ExecuteContext(e.ctx); err != nil {
		t.Fatalf("infraharvest discover google: %v", err)
	}
	return path
}

// checkGoogleImport imports the project with flags, checks the import
// reports no failures and its roots plan no changes, with every resource
// state lists imported, and returns the report.
func checkGoogleImport(ctx context.Context, t *testing.T, engineName, execPath, plugins string, state []*tfjson.StateResource, flags ...string) report.Report {
	t.Helper()
	out := t.TempDir()
	root := cli.NewCmdRoot()
	root.SetArgs(append([]string{
		"import", "google",
		"--engine=" + engineName,
		"--terraform-path=" + execPath,
		"--projects=" + googleProject,
		"--out=" + out,
		"--all",
		"--allow-partial",
	}, flags...))
	if err := root.ExecuteContext(ctx); importer.ExitCode(err) != report.ExitOK && importer.ExitCode(err) != report.ExitPartial {
		t.Fatalf("infraharvest import google %v: %v", flags, err)
	}

	var coverage report.Report
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(out, report.Dir, "coverage.json"))), &coverage); err != nil {
		t.Fatal(err)
	}
	if coverage.Totals.LeftOut != 0 || coverage.Totals.Failed != 0 || len(coverage.Failures) != 0 {
		t.Errorf("%v: coverage.json: %+v, failures %v", flags, coverage.Totals, coverage.Failures)
	}
	for _, d := range coverage.Directories {
		for _, l := range d.LeftOut {
			t.Errorf("%s: %s (%s) left out: %v", d.Path, l.Address, l.ID, l.Errors)
		}
		for _, c := range d.Checks {
			if !c.Passed {
				t.Errorf("%s: %s failed: %v", d.Path, c.Name, c.Details)
			}
		}
	}

	// Only a root with a call of the network module, which creates the
	// network through google-beta, requires google-beta.
	beta := len(moduleResources(coverage, adapters.GoogleNetwork.Source)) > 0
	imported := map[string]int{}
	for _, dir := range generatedDirs(t, out) {
		if !beta && strings.Contains(readFile(t, filepath.Join(dir, "versions.tf")), "google-beta") {
			t.Errorf("%s requires google-beta, which none of its resources uses", dir)
		}
		for typ, n := range checkNoChanges(ctx, t, dir, execPath, plugins, nil) {
			imported[typ] += n
		}
	}
	seeded := map[string]int{}
	for _, r := range state {
		seeded[r.Type]++
	}
	for _, typ := range sortedKeys(seeded) {
		if imported[typ] < seeded[typ] {
			t.Errorf("%v: %s: created %d, imported %d", flags, typ, seeded[typ], imported[typ])
		}
	}
	// An import keeps labels in state only, so a configuration without
	// them plans the same, and the plans above can't tell: each label must
	// be set somewhere, on the resource, in a module call or in a local.
	var config strings.Builder
	for _, dir := range generatedDirs(t, out) {
		config.WriteString(configuration(t, dir))
	}
	for _, r := range state {
		labels, _ := r.AttributeValues["labels"].(map[string]any)
		for key, value := range labels {
			set := regexp.MustCompile(`"?` + regexp.QuoteMeta(key) + `"?\s*=\s*"` + regexp.QuoteMeta(fmt.Sprint(value)) + `"`)
			if !set.MatchString(config.String()) {
				t.Errorf("%v: the configuration doesn't set %s's label %s = %q", flags, r.Address, key, value)
			}
		}
	}
	return coverage
}

// assetSearch stands in for Cloud Asset Inventory's searchAllResources,
// and Cloud DNS's zone list, in front of the emulator at endpoint, which
// gets every other request. The search finds the resources state lists,
// named as Cloud Asset Inventory names them, with the project's number as
// their project, and the default network and one of its firewall rules,
// which Google Cloud creates in a project. It finds only the asset types asked for,
// two at a time. The project has no DNS zones.
func assetSearch(t *testing.T, endpoint string, state []*tfjson.StateResource) *httptest.Server {
	t.Helper()
	target, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	project := "projects/" + googleProject + "/"
	results := []map[string]any{
		{"assetType": "compute.googleapis.com/Network", "name": "//compute.googleapis.com/" + project + "global/networks/default", "location": "global"},
		{"assetType": "compute.googleapis.com/Firewall", "name": "//compute.googleapis.com/" + project + "global/firewalls/default-allow-ssh", "location": "global"},
	}
	for _, r := range state {
		v := r.AttributeValues
		res := map[string]any{"location": "global", "displayName": v["name"]}
		switch r.Type {
		case "google_compute_network":
			res["assetType"], res["name"] = "compute.googleapis.com/Network", "//compute.googleapis.com/"+v["id"].(string)
		case "google_compute_subnetwork":
			res["assetType"], res["name"], res["location"] = "compute.googleapis.com/Subnetwork", "//compute.googleapis.com/"+v["id"].(string), v["region"]
		case "google_compute_firewall":
			res["assetType"], res["name"] = "compute.googleapis.com/Firewall", "//compute.googleapis.com/"+v["id"].(string)
		case "google_storage_bucket":
			res["assetType"], res["name"], res["location"] = "storage.googleapis.com/Bucket", "//storage.googleapis.com/"+v["name"].(string), strings.ToLower(v["location"].(string))
			res["labels"] = v["labels"]
		case "google_pubsub_topic":
			res["assetType"], res["name"] = "pubsub.googleapis.com/Topic", "//pubsub.googleapis.com/"+v["id"].(string)
		case "google_pubsub_subscription":
			res["assetType"], res["name"] = "pubsub.googleapis.com/Subscription", "//pubsub.googleapis.com/"+v["id"].(string)
		case "google_service_account":
			res["assetType"], res["name"] = "iam.googleapis.com/ServiceAccount", "//iam.googleapis.com/projects/"+googleProject+"/serviceAccounts/"+v["unique_id"].(string)
			res["additionalAttributes"] = map[string]any{"email": v["email"]}
		case "google_kms_key_ring":
			res["assetType"], res["name"], res["location"] = "cloudkms.googleapis.com/KeyRing", "//cloudkms.googleapis.com/"+v["id"].(string), v["location"]
		case "google_kms_crypto_key":
			id := v["id"].(string) // projects/<p>/locations/<l>/keyRings/<r>/cryptoKeys/<k>
			res["assetType"], res["name"], res["location"] = "cloudkms.googleapis.com/CryptoKey", "//cloudkms.googleapis.com/"+id, strings.Split(id, "/")[3]
		case "google_project_iam_member":
			continue // a binding in the project's IAM policy, not a resource
		default:
			t.Fatalf("assetSearch: no asset type for %s", r.Type)
		}
		results = append(results, res)
	}
	for _, res := range results {
		res["project"] = "projects/123456789012"
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	stand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dns/v1/"+project+"managedZones" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"managedZones": []}`))
			return
		}
		if !strings.HasSuffix(r.URL.Path, ":searchAllResources") {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.URL.Path != "/v1/"+project[:len(project)-1]+":searchAllResources" {
			http.Error(w, "unexpected scope "+r.URL.Path, http.StatusBadRequest)
			return
		}
		q := r.URL.Query()
		var found []map[string]any
		for _, res := range results {
			if slices.Contains(q["assetTypes"], res["assetType"].(string)) {
				found = append(found, res)
			}
		}
		start, _ := strconv.Atoi(q.Get("pageToken"))
		end := min(start+2, len(found))
		page := map[string]any{"results": found[start:end]}
		if end < len(found) {
			page["nextPageToken"] = strconv.Itoa(end)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	}))
	t.Cleanup(stand.Close)
	return stand
}

// isolateGoogleConfig points the listers (google.EndpointEnv) and the Terraform
// provider (its custom endpoints) at the emulator, with a placeholder token:
// the test never uses a real project or real credentials.
func isolateGoogleConfig(t *testing.T, endpoint string) {
	t.Helper()
	for k, v := range map[string]string{
		"GOOGLE_APPLICATION_CREDENTIALS":          "",
		"GOOGLE_CREDENTIALS":                      "",
		"GOOGLE_OAUTH_ACCESS_TOKEN":               "e2e-placeholder",
		"GOOGLE_CLOUD_PROJECT":                    googleProject,
		"GOOGLE_COMPUTE_CUSTOM_ENDPOINT":          endpoint + "/compute/v1/",
		"GOOGLE_STORAGE_CUSTOM_ENDPOINT":          endpoint + "/storage/v1/",
		"GOOGLE_PUBSUB_CUSTOM_ENDPOINT":           endpoint + "/v1/",
		"GOOGLE_RESOURCE_MANAGER_CUSTOM_ENDPOINT": endpoint + "/v1/",
		"GOOGLE_SERVICE_USAGE_CUSTOM_ENDPOINT":    endpoint + "/v1/",
		"GOOGLE_IAM_BETA_CUSTOM_ENDPOINT":         endpoint + "/v1/", // service accounts
		"GOOGLE_KMS_CUSTOM_ENDPOINT":              endpoint + "/v1/",
	} {
		t.Setenv(k, v)
	}
}

// checkGoogleModules checks that resources of each type of want, by module,
// became calls of the curated module, as did the bucket and the key ring
// with its keys; the plans check that changes nothing.
func checkGoogleModules(t *testing.T, coverage report.Report, want map[string][]string) {
	t.Helper()
	members := map[string][]string{}
	for _, d := range coverage.Directories {
		for _, m := range d.Modules {
			if m.Declined == "" {
				members[m.Source] = append(members[m.Source], m.Resources...)
			}
		}
	}
	want[adapters.GoogleStorageBucket.Source] = []string{"google_storage_bucket"}
	want[adapters.GoogleKMS.Source] = []string{"google_kms_key_ring", "google_kms_crypto_key"}
	for _, source := range slices.Sorted(maps.Keys(want)) {
		for _, typ := range want[source] {
			if !slices.ContainsFunc(members[source], func(address string) bool { return strings.HasPrefix(address, typ+".") }) {
				t.Errorf("no %s in a call of %s; modules:", typ, source)
				for _, d := range coverage.Directories {
					for _, m := range d.Modules {
						t.Logf("%s: %+v", d.Path, m)
					}
				}
			}
		}
	}
}

// moduleResources returns the resources in the kept calls of source.
func moduleResources(coverage report.Report, source string) []string {
	var resources []string
	for _, d := range coverage.Directories {
		for _, m := range d.Modules {
			if m.Source == source && m.Declined == "" {
				resources = append(resources, m.Resources...)
			}
		}
	}
	return resources
}
