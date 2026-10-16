// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"reflect"
	"strings"
	"testing"
)

const googleProjectIAMCluster = `resource "google_project_iam_member" "viewer_alice" {
  member  = "user:alice@example.com"
  project = "acme"
  role    = "roles/viewer"
}
resource "google_project_iam_member" "auditor_alice" {
  member  = "user:alice@example.com"
  project = "acme"
  role    = "roles/iam.securityReviewer"
  condition {
    expression = "request.time < timestamp(\"2030-01-01T00:00:00Z\")"
    title      = "until-2030"
  }
}
resource "google_project_iam_member" "viewer_ops" {
  member  = "group:ops@example.com"
  project = "acme"
  role    = "roles/viewer"
}
`

func TestGoogleProjectIAMMapsTheGrants(t *testing.T) {
	call, err := GoogleProjectIAM.Map(cluster(t, googleProjectIAMCluster))
	if err != nil {
		t.Fatal(err)
	}
	got := render(t, GoogleProjectIAM, call)
	for _, want := range []string{
		`projects = ["acme"]`,
		`bindings = { "roles/viewer" = ["group:ops@example.com", "user:alice@example.com"] }`,
		`conditional_bindings = [{ description = "" expression = "request.time < timestamp(\"2030-01-01T00:00:00Z\")" members = ["user:alice@example.com"] role = "roles/iam.securityReviewer" title = "until-2030" }]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("call misses %q:\n%s", want, got)
		}
	}
	want := map[string]string{
		"google_project_iam_member.viewer_alice":  `google_project_iam_member.project_iam_additive["default--roles/viewer--user:alice@example.com"]`,
		"google_project_iam_member.viewer_ops":    `google_project_iam_member.project_iam_additive["default--roles/viewer--group:ops@example.com"]`,
		"google_project_iam_member.auditor_alice": `google_project_iam_member.project_iam_additive["default--roles/iam.securityReviewer--until-2030--user:alice@example.com"]`,
	}
	if !reflect.DeepEqual(call.Addresses, want) {
		t.Errorf("addresses: %v", call.Addresses)
	}
	// Not after the anchor, viewer_alice, one grant of the three.
	if call.Name != "project_iam" {
		t.Errorf("call named %q, want project_iam", call.Name)
	}
}

func TestGoogleProjectIAMDeclines(t *testing.T) {
	for name, tc := range map[string]struct{ from, to, reason string }{
		"a grant in another project": {
			`member  = "group:ops@example.com"
  project = "acme"`, `member  = "group:ops@example.com"
  project = "other"`,
			"grants a role in another project",
		},
		"two conditions with one title": {
			`role    = "roles/viewer"
}
resource "google_project_iam_member" "auditor_alice"`, `role    = "roles/iam.securityReviewer"
  condition {
    expression = "true"
    title      = "until-2030"
  }
}
resource "google_project_iam_member" "auditor_alice"`,
			`two conditions titled "until-2030"`,
		},
		"a condition without a literal expression": {
			`expression = "request.time < timestamp(\"2030-01-01T00:00:00Z\")"`, `expression = local.until`,
			"without a literal expression",
		},
	} {
		t.Run(name, func(t *testing.T) {
			src := strings.Replace(googleProjectIAMCluster, tc.from, tc.to, 1)
			if src == googleProjectIAMCluster {
				t.Fatal("the test doesn't change the source")
			}
			_, err := GoogleProjectIAM.Map(cluster(t, src))
			if !IsDecline(err) || !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("got %v, want a decline with %q", err, tc.reason)
			}
		})
	}
}

// Grants are grouped by project, and only those with a literal role and
// member: the module's keys spell them out.
func TestGoogleProjectIAMGroups(t *testing.T) {
	c := cluster(t, `resource "google_project_iam_member" "a" {
  member  = "user:alice@example.com"
  project = "acme"
  role    = "roles/viewer"
}
resource "google_project_iam_member" "b" {
  member  = google_service_account.ci.member
  project = "acme"
  role    = "roles/viewer"
}
resource "google_project_iam_member" "c" {
  member  = "user:alice@example.com"
  project = local.project
  role    = "roles/viewer"
}
`)
	for _, tc := range []struct {
		r    Resource
		key  string
		want bool
	}{
		{c.Anchor, `"acme"`, true},
		{c.Members[0], "", false},
		{c.Members[1], "local.project", true},
	} {
		if key, ok := GoogleProjectIAM.Group(tc.r); key != tc.key || ok != tc.want {
			t.Errorf("%s: got %q, %v", tc.r.Address(), key, ok)
		}
	}
}
