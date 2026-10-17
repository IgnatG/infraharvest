// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"reflect"
	"strings"
	"testing"
)

const googleServiceAccountCluster = `resource "google_service_account" "ci" {
  account_id                   = "ci"
  create_ignore_already_exists = false
  disabled                     = false
  display_name                 = "CI"
  project                      = "acme"
}
resource "google_project_iam_member" "ci_subscriber" {
  member  = google_service_account.ci.member
  project = "acme"
  role    = "roles/pubsub.subscriber"
}
resource "google_project_iam_member" "ci_viewer" {
  member  = google_service_account.ci.member
  project = "acme"
  role    = "roles/viewer"
}
`

func TestGoogleServiceAccountMapsItsRoles(t *testing.T) {
	t.Parallel()
	call, err := GoogleServiceAccount.Map(cluster(t, googleServiceAccountCluster))
	if err != nil {
		t.Fatal(err)
	}
	got := render(t, GoogleServiceAccount, call)
	for _, want := range []string{
		`project_id = "acme"`,
		`name = "ci"`,
		`display_name = "CI"`,
		`description = ""`,
		`project_roles = ["roles/pubsub.subscriber", "roles/viewer"]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("call misses %q:\n%s", want, got)
		}
	}
	want := map[string]string{
		"google_service_account.ci":               "google_service_account.sa",
		"google_project_iam_member.ci_subscriber": `google_project_iam_member.roles["roles/pubsub.subscriber"]`,
		"google_project_iam_member.ci_viewer":     `google_project_iam_member.roles["roles/viewer"]`,
	}
	if !reflect.DeepEqual(call.Addresses, want) {
		t.Errorf("addresses: %v", call.Addresses)
	}
}

func TestGoogleServiceAccountDeclines(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ from, to, reason string }{
		"a role in another project": {
			`project = "acme"
  role    = "roles/viewer"`, `project = "other"
  role    = "roles/viewer"`,
			"grants a role in another project",
		},
		"a conditional grant": {
			`role    = "roles/viewer"`, `role    = "roles/viewer"
  condition {
    expression = "request.time < timestamp(\"2030-01-01T00:00:00Z\")"
    title      = "until-2030"
  }`,
			"the module can't set google_project_iam_member.ci_viewer.condition",
		},
		"a disabled account": {
			"disabled                     = false", "disabled = true",
			"the module can't set google_service_account.ci.disabled",
		},
	} {
		t.Run(name, func(t *testing.T) {
			src := strings.Replace(googleServiceAccountCluster, tc.from, tc.to, 1)
			if src == googleServiceAccountCluster {
				t.Fatal("the test doesn't change the source")
			}
			_, err := GoogleServiceAccount.Map(cluster(t, src))
			if !IsDecline(err) || !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("got %v, want a decline with %q", err, tc.reason)
			}
		})
	}
}
