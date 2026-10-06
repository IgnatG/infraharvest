// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package managed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// fakeWorkspaces are an organization's workspaces and their state, nil
// for none.
type fakeWorkspaces map[string][]byte

func (f fakeWorkspaces) List(_ context.Context, _, prefix string) ([]string, error) {
	var names []string
	for name := range f {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	return names, nil
}

func (f fakeWorkspaces) Get(_ context.Context, _, name string) ([]byte, error) {
	state, ok := f[name]
	if !ok {
		return nil, fmt.Errorf("no workspace %s", name)
	}
	return state, nil
}

func TestLoadWorkspaces(t *testing.T) {
	stateOf := func(id string) []byte {
		return []byte(`{"version": 4, "resources": [{"mode": "managed", "type": "aws_s3_bucket", "instances": [{"attributes": {"id": "` + id + `"}}]}]}`)
	}
	workspaces := fakeWorkspaces{
		"network-prod":  stateOf("network"),
		"network-stage": nil, // no state yet
		"app-prod":      stateOf("app"),
	}
	var hosts []string
	stores := Stores{HCPTerraform: func(_ context.Context, host string) (ObjectStore, error) {
		hosts = append(hosts, host)
		return workspaces, nil
	}}

	r, err := Load(t.Context(), []string{"tfc://acme/network-*", "tfc://acme/app-prod?host=tfe.example.com"}, stores)
	if err != nil {
		t.Fatal(err)
	}
	if where, _ := r.Lookup("aws_s3_bucket", "network"); where != "tfc://acme/network-prod" {
		t.Errorf("network: %q", where)
	}
	if where, _ := r.Lookup("aws_s3_bucket", "app"); where != "tfc://tfe.example.com/acme/app-prod" {
		t.Errorf("app: %q", where)
	}
	if strings.Join(hosts, ",") != DefaultHCPTerraformHost+",tfe.example.com" {
		t.Errorf("hosts: %v", hosts)
	}

	for _, source := range []string{"tfc://acme/missing", "tfc://acme", "tfc:///network-prod"} {
		if _, err := Load(t.Context(), []string{source}, stores); err == nil {
			t.Errorf("%s: want an error", source)
		}
	}
}

func TestHCPTerraform(t *testing.T) {
	const token = "api-token"
	state := `{"version": 4, "resources": []}`
	// State downloads are signed URLs on another host, which must not get
	// the token.
	archivist := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("the token went to the download host")
		}
		_, _ = w.Write([]byte(state))
	}))
	defer archivist.Close()

	names := []string{"network-prod", "network-stage", "app-network-x"}
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body any
		switch path := r.URL.Path; {
		case path == "/api/v2/organizations/acme/workspaces":
			// One workspace a page. The search matches anywhere in the name,
			// as the API's does: app-network-x is left to List to drop.
			page, _ := strconv.Atoi(r.URL.Query().Get("page[number]"))
			var data []any
			if page <= len(names) && strings.Contains(names[page-1], r.URL.Query().Get("search[name]")) {
				data = append(data, map[string]any{"attributes": map[string]any{"name": names[page-1]}})
			}
			next := page + 1
			if next > len(names) {
				next = 0
			}
			body = map[string]any{"data": data, "meta": map[string]any{"pagination": map[string]any{"next-page": next}}}
		case strings.HasPrefix(path, "/api/v2/organizations/acme/workspaces/"):
			body = map[string]any{"data": map[string]any{"id": "ws-" + strings.TrimPrefix(path, "/api/v2/organizations/acme/workspaces/")}}
		case path == "/api/v2/workspaces/ws-network-prod/current-state-version":
			body = map[string]any{"data": map[string]any{"attributes": map[string]any{"hosted-state-download-url": archivist.URL + "/v1/object/signed"}}}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer api.Close()
	h := &HCPTerraform{client: api.Client(), base: api.URL + "/api/v2", token: token}

	got, err := h.List(t.Context(), "acme", "network-")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "network-prod,network-stage" {
		t.Errorf("List: %v", got)
	}
	content, err := h.Get(t.Context(), "acme", "network-prod")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != state {
		t.Errorf("Get: %q", content)
	}
	// No current state version yet.
	if content, err := h.Get(t.Context(), "acme", "network-stage"); err != nil || content != nil {
		t.Errorf("Get without state: %q, %v", content, err)
	}
	h.token = "wrong"
	if _, err := h.List(t.Context(), "acme", ""); err == nil {
		t.Error("want an error for a rejected token")
	}
}

func TestHCPTerraformToken(t *testing.T) {
	dir := t.TempDir()
	// terraform login's file: under %APPDATA% on Windows, the home
	// directory elsewhere.
	credentials := filepath.Join(dir, ".terraform.d", "credentials.tfrc.json")
	if runtime.GOOS == "windows" {
		credentials = filepath.Join(dir, "terraform.d", "credentials.tfrc.json")
	}
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Dir(credentials), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentials, []byte(`{"credentials": {"app.terraform.io": {"token": "from-login"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"APPDATA": dir, "TF_TOKEN_tfe_my__corp_example": "from-env"}
	getenv := func(k string) string { return env[k] }

	for host, want := range map[string]string{
		"app.terraform.io":    "from-login",
		"tfe.my-corp.example": "from-env",
	} {
		got, err := hcpTerraformToken(host, getenv)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if got != want {
			t.Errorf("%s: token %q, want %q", host, got, want)
		}
	}
	if _, err := hcpTerraformToken("tfe.other.example", getenv); err == nil || !strings.Contains(err.Error(), "TF_TOKEN_tfe_other_example") {
		t.Errorf("want an error naming TF_TOKEN_tfe_other_example, got %v", err)
	}
}
