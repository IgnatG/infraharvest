// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

func TestIamInitResources(t *testing.T) {
	var policyVersion float64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/projects/acme/serviceAccounts" && r.URL.Query().Get("pageToken") == "":
			_, _ = w.Write([]byte(`{"accounts": [{"name": "projects/acme/serviceAccounts/app@acme.iam.gserviceaccount.com", "email": "app@acme.iam.gserviceaccount.com", "uniqueId": "1"}], "nextPageToken": "2"}`))
		case r.URL.Path == "/v1/projects/acme/serviceAccounts" && r.URL.Query().Get("pageToken") == "2":
			_, _ = w.Write([]byte(`{"accounts": [{"name": "projects/acme/serviceAccounts/ci@acme.iam.gserviceaccount.com", "email": "ci@acme.iam.gserviceaccount.com", "uniqueId": "2"}, {"name": "projects/acme/serviceAccounts/1234-compute@developer.gserviceaccount.com", "email": "1234-compute@developer.gserviceaccount.com", "uniqueId": "3"}]}`))
		case r.URL.Path == "/v1/projects/acme/roles":
			_, _ = w.Write([]byte(`{"roles": [{"name": "projects/acme/roles/deployer"}, {"name": "projects/acme/roles/old", "deleted": true}]}`))
		case r.URL.Path == "/v1/projects/acme:getIamPolicy" && r.Method == http.MethodPost:
			var body struct {
				Options struct {
					RequestedPolicyVersion float64 `json:"requestedPolicyVersion"`
				} `json:"options"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			policyVersion = body.Options.RequestedPolicyVersion
			_, _ = w.Write([]byte(`{"version": 3, "bindings": [
				{"role": "roles/viewer", "members": ["user:ana@acme.example", "group:ops@acme.example"]},
				{"role": "roles/editor", "members": ["user:ana@acme.example"], "condition": {"title": "expires_2027", "expression": "request.time < timestamp('2027-01-01T00:00:00Z')"}}
			]}`))
		default:
			http.Error(w, r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv(EndpointEnv, server.URL)

	g := &IamGenerator{}
	g.SetArgs(map[string]interface{}{"project": "acme"})
	g.SetContext(t.Context())
	if err := g.InitResources(); err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, r := range g.Resources {
		got = append(got, r.InstanceInfo.Type+" "+r.InstanceState.ID)
	}
	sort.Strings(got)
	want := []string{
		"google_project_iam_custom_role projects/acme/roles/deployer",
		"google_project_iam_member acme roles/editor user:ana@acme.example expires_2027",
		"google_project_iam_member acme roles/viewer group:ops@acme.example",
		"google_project_iam_member acme roles/viewer user:ana@acme.example",
		"google_service_account projects/acme/serviceAccounts/app@acme.iam.gserviceaccount.com",
		"google_service_account projects/acme/serviceAccounts/ci@acme.iam.gserviceaccount.com",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("resources:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, r := range g.Resources {
		if r.InstanceInfo.Type == "google_project_iam_custom_role" && r.InstanceState.Attributes["role_id"] != "deployer" {
			t.Errorf("role_id %q", r.InstanceState.Attributes["role_id"])
		}
	}
	if policyVersion != 3 {
		t.Errorf("requested policy version %v, want 3 for conditional bindings", policyVersion)
	}
}

// A failing page is an error, not a loop.
func TestIamInitResourcesFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error": {"code": 403, "message": "denied"}}`, http.StatusForbidden)
	}))
	defer server.Close()
	t.Setenv(EndpointEnv, server.URL)

	g := &IamGenerator{}
	g.SetArgs(map[string]interface{}{"project": "acme"})
	g.SetContext(t.Context())
	if err := g.InitResources(); err == nil {
		t.Error("want an error")
	}
}
