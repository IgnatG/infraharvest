// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// Before, every Google resource repeated the provider block's project
// (and, in a region's root, its region). They go where they repeat the
// provider's value and the schema has them optional and computed; other
// values, required arguments, aliased providers and types the schema
// doesn't describe keep theirs.
func TestOmitInherited(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.ProvidersFileName, `provider "google" {
  add_terraform_attribution_label = false
  project                         = "acme"
  region                          = "europe-west2"
}

provider "google" {
  alias   = "us"
  project = "acme"
  region  = "us-central1"
}
`)
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "google_pubsub_topic" "orders" {
  name    = "orders"
  project = "acme"
}

resource "google_pubsub_topic" "shared" {
  name    = "shared"
  project = "elsewhere"
}

resource "google_project_iam_member" "ana_owner" {
  member  = "user:ana@acme.example"
  project = "acme"
  role    = "roles/owner"
}

resource "google_compute_subnetwork" "app" {
  name    = "app"
  project = "acme"
  region  = "europe-west2"
}

resource "google_compute_subnetwork" "us" {
  provider = google.us
  name     = "us"
  project  = "acme"
  region   = "us-central1"
}

resource "google_dns_record_set" "app" {
  name    = "app.example.com."
  project = "acme"
}
`)
	optional := &tfjson.SchemaAttribute{Optional: true, Computed: true}
	block := func(attributes map[string]*tfjson.SchemaAttribute) *tfjson.Schema {
		return &tfjson.Schema{Block: &tfjson.SchemaBlock{Attributes: attributes}}
	}
	tf := &fakeTerraform{dir: dir, schemas: &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
		"registry.terraform.io/hashicorp/google": {ResourceSchemas: map[string]*tfjson.Schema{
			"google_pubsub_topic":       block(map[string]*tfjson.SchemaAttribute{"project": optional}),
			"google_project_iam_member": block(map[string]*tfjson.SchemaAttribute{"project": {Required: true}}),
			"google_compute_subnetwork": block(map[string]*tfjson.SchemaAttribute{"project": optional, "region": optional}),
		}},
	}}}

	changed, err := omitInherited(context.Background(), tf, dir, []string{"project", "region", "zone"})
	if err != nil || !changed {
		t.Fatalf("changed %v, err %v", changed, err)
	}
	generated := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	for _, want := range []string{
		`resource "google_pubsub_topic" "orders" { name = "orders" }`,
		`resource "google_pubsub_topic" "shared" { name = "shared" project = "elsewhere" }`,
		`resource "google_project_iam_member" "ana_owner" { member = "user:ana@acme.example" project = "acme" role = "roles/owner" }`,
		`resource "google_compute_subnetwork" "app" { name = "app" }`,
		`resource "google_compute_subnetwork" "us" { provider = google.us name = "us" project = "acme" region = "us-central1" }`,
		`resource "google_dns_record_set" "app" { name = "app.example.com." project = "acme" }`,
	} {
		if !strings.Contains(generated, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, generated)
		}
	}
}

// Without a value to repeat, the schema isn't read.
func TestOmitInheritedWithoutProviderValues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.ProvidersFileName, "provider \"google\" {\n  add_terraform_attribution_label = false\n}\n")
	writeConfig(t, dir, rootfile.GeneratedFileName, "resource \"google_pubsub_topic\" \"orders\" {\n  project = \"acme\"\n}\n")
	tf := &fakeTerraform{dir: dir}
	if changed, err := omitInherited(context.Background(), tf, dir, []string{"project"}); changed || err != nil {
		t.Errorf("changed %v, err %v", changed, err)
	}
	if len(tf.calls) != 0 {
		t.Errorf("calls: %v", tf.calls)
	}
}
