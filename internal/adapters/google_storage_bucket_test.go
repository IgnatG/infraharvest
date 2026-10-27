// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"strings"
	"testing"
)

const googleBucketCluster = `resource "google_storage_bucket" "assets" {
  deletion_policy             = "DELETE"
  force_destroy               = true
  location                    = "US"
  name                        = "acme-assets"
  project                     = "acme"
  public_access_prevention    = "inherited"
  requester_pays              = false
  rpo                         = "DEFAULT"
  storage_class               = "STANDARD"
  uniform_bucket_level_access = true
  lifecycle_rule {
    action {
      type = "Delete"
    }
    condition {
      age            = 30
      matches_prefix = ["logs/", "tmp/"]
      with_state     = "ANY"
    }
  }
  soft_delete_policy {
    effective_time             = "2026-10-01T00:00:00Z"
    retention_duration_seconds = 604800
  }
}
`

func TestGoogleStorageBucketMaps(t *testing.T) {
	t.Parallel()
	c := cluster(t, googleBucketCluster)
	call, err := googleStorageBucket.Map(c)
	if err != nil {
		t.Fatal(err)
	}
	got := render(t, googleStorageBucket, call)
	for _, want := range []string{
		`name = "acme-assets"`,
		`project_id = "acme"`,
		`location = "US"`,
		"force_destroy = true",
		`storage_class = "STANDARD"`,
		"bucket_policy_only = true",
		// Off unless the bucket has it on: the module's default is on.
		"versioning = false",
		"soft_delete_policy = { retention_duration_seconds = 604800 }",
		`lifecycle_rules = [{ action = { type = "Delete" } condition = { age = 30 with_state = "ANY" matches_prefix = "logs/,tmp/" } }]`,
	} {
		if !strings.Contains(got, squash(want)) {
			t.Errorf("call misses %q:\n%s", want, got)
		}
	}
	if call.Addresses["google_storage_bucket.assets"] != "google_storage_bucket.bucket" {
		t.Errorf("addresses: %v", call.Addresses)
	}
}

func TestGoogleStorageBucketDeclines(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ from, to string }{
		"requester pays":    {"requester_pays              = false", "requester_pays = true"},
		"turbo replication": {`rpo                         = "DEFAULT"`, `rpo = "ASYNC_TURBO"`},
		"no project":        {`project                     = "acme"`, ""},
		"an ip filter":      {"storage_class               = \"STANDARD\"", "storage_class = \"STANDARD\"\n  ip_filter { mode = \"Enabled\" }"},
		"a comma":           {`matches_prefix = ["logs/", "tmp/"]`, `matches_prefix = ["a,b"]`},
		"a deletion policy": {`deletion_policy             = "DELETE"`, `deletion_policy = "ABANDON"`},
	} {
		src := strings.Replace(googleBucketCluster, tc.from, tc.to, 1)
		if src == googleBucketCluster {
			t.Fatalf("%s: the replacement doesn't apply", name)
		}
		if _, err := googleStorageBucket.Map(cluster(t, src)); !IsDecline(err) {
			t.Errorf("%s: got %v, want a decline", name, err)
		}
	}
}
