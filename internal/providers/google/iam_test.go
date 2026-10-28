// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"google.golang.org/api/compute/v1"
)

// The project's IAM members, one per member of each binding, a
// conditional one with its condition's title. Service accounts and custom
// roles come from cloud_asset.
func TestIamList(t *testing.T) {
	var policyVersion float64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
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

	g := &IamLister{}
	g.SetSettings(listerSettings{project: "acme"})
	g.SetContext(t.Context())
	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, r := range g.Resources {
		got = append(got, r.Address.Type+" "+r.Observed.ID)
	}
	sort.Strings(got)
	want := []string{
		"google_project_iam_member acme roles/editor user:ana@acme.example expires_2027",
		"google_project_iam_member acme roles/viewer group:ops@acme.example",
		"google_project_iam_member acme roles/viewer user:ana@acme.example",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("resources:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, r := range g.Resources {
		if a := r.Observed.Attributes; a["project"] != "acme" || a["role"] == "" || a["member"] == "" {
			t.Errorf("%s: attributes %v", r.Observed.ID, a)
		}
	}
	if policyVersion != 3 {
		t.Errorf("requested policy version %v, want 3 for conditional bindings", policyVersion)
	}
	var names []string
	for _, r := range g.Resources {
		names = append(names, r.RawName)
	}
	sort.Strings(names)
	if want := []string{"ana_editor_expires_2027", "ana_viewer", "ops_viewer"}; strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("names %v, want %v", names, want)
	}
}

// A grant is named after the member's short name and the role's, as an
// AWS policy attachment is after its role and policy. Before, it was named
// after the role and the whole member, such as
// roles_viewer_serviceaccount_deployer_acme_iam_gserviceaccount_com.
func TestGrantName(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ role, member, title, want string }{
		{"roles/viewer", "serviceAccount:deployer@acme.iam.gserviceaccount.com", "", "deployer_viewer"},
		{"roles/cloudscheduler.serviceAgent", "serviceAccount:service-123@gcp-sa-cloudscheduler.iam.gserviceaccount.com", "", "service-123_cloudscheduler.serviceAgent"},
		{"projects/acme/roles/deployer", "group:ops@acme.example", "", "ops_deployer"},
		{"roles/owner", "user:ana@acme.example", "until_2027", "ana_owner_until_2027"},
		{"roles/viewer", "domain:acme.example", "", "acme.example_viewer"},
		{"roles/storage.objectViewer", "allUsers", "", "allUsers_storage.objectViewer"},
		{"roles/viewer", "deleted:serviceAccount:ci@acme.iam.gserviceaccount.com?uid=123", "", "ci_viewer"},
		{"roles/run.invoker", "principal://iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/gh/subject/build@acme.example", "", "build_run.invoker"},
		{"roles/run.invoker", "principalSet://iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/gh/attribute.repository/acme/shop", "", "shop_run.invoker"},
	} {
		if got := grantName(tt.role, tt.member, tt.title); got != tt.want {
			t.Errorf("grantName(%q, %q, %q): got %q, want %q", tt.role, tt.member, tt.title, got, tt.want)
		}
	}
}

// The policy is the project's, in the import of global alone.
func TestIamListsNothingInARegion(t *testing.T) {
	t.Setenv(EndpointEnv, "http://127.0.0.1:1")
	g := &IamLister{}
	g.SetSettings(listerSettings{project: "acme", region: compute.Region{Name: "us-central1"}})
	g.SetContext(t.Context())
	if err := g.List(); err != nil || len(g.Resources) != 0 {
		t.Errorf("got %v, %v", g.Resources, err)
	}
}

// A failing page is an error, not a loop.
func TestIamListFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error": {"code": 403, "message": "denied"}}`, http.StatusForbidden)
	}))
	defer server.Close()
	t.Setenv(EndpointEnv, server.URL)

	g := &IamLister{}
	g.SetSettings(listerSettings{project: "acme"})
	g.SetContext(t.Context())
	if err := g.List(); err == nil {
		t.Error("want an error")
	}
}
