// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// acceptRoot writes files, by slash-separated paths, as a root at
// aws/123/eu-west-2 of an output directory, and returns the root and
// where Drift stages it.
func acceptRoot(t *testing.T, files map[string]string) (dir, staging string) {
	t.Helper()
	out := t.TempDir()
	dir = filepath.Join(out, "aws", "123", "eu-west-2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, filepath.Join(out, ".infraharvest", "d")
}

// tagChange is a resource whose tag Env goes from one value to another.
func tagChange(address string, actions tfjson.Actions, from, to string) *tfjson.ResourceChange {
	typ, _, _ := strings.Cut(address, ".")
	return driftResource(address, typ, actions,
		map[string]any{"tags": map[string]any{"Env": from}, "tags_all": map[string]any{"Env": from}},
		map[string]any{"tags": map[string]any{"Env": to}, "tags_all": map[string]any{"Env": to}})
}

func noChange(address string) *tfjson.ResourceChange {
	return tagChange(address, tfjson.Actions{tfjson.ActionNoop}, "dev", "dev")
}

// acceptAll runs Drift on the root in dir accepting every change, with
// the staged copy's plans: the refresh, the code's, then one per round of
// verification.
func acceptAll(t *testing.T, dir, staging string, plans []*tfjson.Plan, opts DriftOptions) (*RootDrift, *fakeDriftTerraform, []string, error) {
	t.Helper()
	var calls []string
	root := &fakeDriftTerraform{name: "root", calls: &calls, state: driftState}
	copied := &fakeDriftTerraform{name: "staged", calls: &calls, plans: plans}
	opts.Accept = func(DriftChange) bool { return true }
	result, err := Drift(context.Background(), root, dir, staging, func(string) (DriftTerraform, error) { return copied, nil }, opts)
	return result, copied, calls, err
}

func outcomes(result *RootDrift) map[string]DriftChange {
	got := map[string]DriftChange{}
	for _, c := range result.Changes {
		got[c.Address+" "+c.Attribute] = c
	}
	return got
}

const taggedBuckets = `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags = {
    Env = "prod"
  }
}

resource "aws_s3_bucket" "data" {
  bucket = "data"
  tags = {
    Env = "prod"
  }
}
`

// An edit the plan doesn't take is left out, and the others are planned
// again and written; the cloud is refreshed once.
func TestDriftAcceptDropsAnEditThePlanRejects(t *testing.T) {
	t.Parallel()
	dir, staging := acceptRoot(t, map[string]string{"main.tf": taggedBuckets})
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
		tagChange("aws_s3_bucket.logs", tfjson.Actions{tfjson.ActionUpdate}, "prod", "dev"),
		tagChange("aws_s3_bucket.data", tfjson.Actions{tfjson.ActionUpdate}, "prod", "dev"),
	}}
	dataReverts := tagChange("aws_s3_bucket.data", tfjson.Actions{tfjson.ActionUpdate}, "dev", "prod")
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		tagChange("aws_s3_bucket.logs", tfjson.Actions{tfjson.ActionUpdate}, "dev", "prod"),
		dataReverts,
	}}
	// The first plan of the edits still changes data's tag; without that
	// edit, data plans what the code did.
	first := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{noChange("aws_s3_bucket.logs"), dataReverts}}
	second := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{noChange("aws_s3_bucket.logs"), dataReverts}}
	result, copied, calls, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, first, second}, DriftOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(result)
	if c := got["aws_s3_bucket.logs tags.Env"]; c.Outcome != DriftWritten || c.Decision != DriftAccept {
		t.Errorf("logs: %s (%s): %s", c.Outcome, c.Decision, c.Reason)
	}
	if c := got["aws_s3_bucket.data tags.Env"]; c.Outcome != DriftRejectedByPlan || !strings.Contains(c.Reason, "still changes tags.Env") {
		t.Errorf("data: %s: %s", c.Outcome, c.Reason)
	}
	want := strings.Replace(taggedBuckets, `Env = "prod"`, `Env = "dev"`, 1)
	if got := readFile(t, dir, "main.tf"); got != want {
		t.Errorf("main.tf:\n%s\nwant:\n%s", got, want)
	}
	if len(copied.planOpts) != 4 {
		t.Fatalf("%d plans, want 4", len(copied.planOpts))
	}
	for i, opts := range copied.planOpts {
		refreshes := hasOption(opts, tfexec.PlanOption(tfexec.RefreshOnly(true)))
		if refreshes != (i == 0) {
			t.Errorf("plan %d refreshes: %v", i, refreshes)
		}
		if i > 0 && (!hasOption(opts, tfexec.PlanOption(tfexec.Refresh(false))) || !hasOption(opts, tfexec.PlanOption(tfexec.Lock(false)))) {
			t.Errorf("plan %d isn't -refresh=false -lock=false: %#v", i, opts)
		}
	}
	schemas := 0
	for _, c := range calls {
		if c == "staged:schema" {
			schemas++
		}
	}
	if schemas != 1 {
		t.Errorf("read the schemas %d times: %v", schemas, calls)
	}
}

// An edit of a local that reaches another resource is caught by that
// resource's change, and left out; the other edits are written.
func TestDriftAcceptCatchesASharedEdit(t *testing.T) {
	t.Parallel()
	const main = `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags   = local.logs_tags
}

resource "aws_s3_bucket" "data" {
  bucket = "data"
  tags = {
    Env = aws_s3_bucket.logs.tags["Env"]
  }
}

resource "aws_sqs_queue" "jobs" {
  name = "jobs"
  tags = {
    Env = "prod"
  }
}
`
	const locals = `locals {
  logs_tags = {
    Env = "prod"
  }
}
`
	dir, staging := acceptRoot(t, map[string]string{"main.tf": main, "locals.tf": locals})
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
		tagChange("aws_s3_bucket.logs", tfjson.Actions{tfjson.ActionUpdate}, "prod", "dev"),
		tagChange("aws_sqs_queue.jobs", tfjson.Actions{tfjson.ActionUpdate}, "prod", "dev"),
	}}
	logsReverts := tagChange("aws_s3_bucket.logs", tfjson.Actions{tfjson.ActionUpdate}, "dev", "prod")
	data := noChange("aws_s3_bucket.data")
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{logsReverts, data, tagChange("aws_sqs_queue.jobs", tfjson.Actions{tfjson.ActionUpdate}, "dev", "prod")}}
	first := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		noChange("aws_s3_bucket.logs"),
		tagChange("aws_s3_bucket.data", tfjson.Actions{tfjson.ActionUpdate}, "prod", "dev"),
		noChange("aws_sqs_queue.jobs"),
	}}
	second := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{logsReverts, data, noChange("aws_sqs_queue.jobs")}}
	result, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, first, second}, DriftOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(result)
	if c := got["aws_s3_bucket.logs tags.Env"]; c.Outcome != DriftRejectedByPlan || !strings.Contains(c.Reason, "aws_s3_bucket.data") {
		t.Errorf("logs: %s: %s", c.Outcome, c.Reason)
	}
	if c := got["aws_sqs_queue.jobs tags.Env"]; c.Outcome != DriftWritten {
		t.Errorf("jobs: %s: %s", c.Outcome, c.Reason)
	}
	if got := readFile(t, dir, "locals.tf"); got != locals {
		t.Errorf("locals.tf changed:\n%s", got)
	}
	want := strings.Replace(main, "Env = \"prod\"", "Env = \"dev\"", 1)
	if got := readFile(t, dir, "main.tf"); got != want {
		t.Errorf("main.tf:\n%s\nwant:\n%s", got, want)
	}
}

// If writing an edited file into the root fails, the files written before
// it are restored.
func TestDriftAcceptRestoresTheRootIfWritingFails(t *testing.T) {
	t.Parallel()
	const main = `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags   = local.logs_tags
}

resource "aws_sqs_queue" "jobs" {
  name = "jobs"
  tags = {
    Env = "prod"
  }
}
`
	const locals = `locals {
  logs_tags = {
    Env = "prod"
  }
}
`
	dir, staging := acceptRoot(t, map[string]string{"main.tf": main, "locals.tf": locals})
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
		tagChange("aws_s3_bucket.logs", tfjson.Actions{tfjson.ActionUpdate}, "prod", "dev"),
		tagChange("aws_sqs_queue.jobs", tfjson.Actions{tfjson.ActionUpdate}, "prod", "dev"),
	}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		tagChange("aws_s3_bucket.logs", tfjson.Actions{tfjson.ActionUpdate}, "dev", "prod"),
		tagChange("aws_sqs_queue.jobs", tfjson.Actions{tfjson.ActionUpdate}, "dev", "prod"),
	}}
	edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{noChange("aws_s3_bucket.logs"), noChange("aws_sqs_queue.jobs")}}
	var wrote []string
	opts := DriftOptions{writeFile: func(path string, content []byte, perm os.FileMode) error {
		wrote = append(wrote, filepath.Base(path))
		if filepath.Base(path) == "main.tf" {
			return errors.New("disk full")
		}
		return fsutil.WriteFile(path, content, perm)
	}}
	_, _, _, err := acceptAll(t, dir, staging, []*tfjson.Plan{refresh, code, edited}, opts)
	if err == nil || !strings.Contains(err.Error(), "disk full") || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("got %v, want the write's error, with the root restored", err)
	}
	if len(wrote) != 2 || wrote[0] != "locals.tf" {
		t.Errorf("wrote %v, want locals.tf then main.tf", wrote)
	}
	for name, want := range map[string]string{"main.tf": main, "locals.tf": locals} {
		if got := readFile(t, dir, name); got != want {
			t.Errorf("%s isn't restored:\n%s", name, got)
		}
	}
}

// Edits that fail a check of the verification gate aren't written.
func TestDriftAcceptChecksTheEditedCode(t *testing.T) {
	t.Parallel()
	dir, staging := acceptRoot(t, map[string]string{"main.tf": taggedBuckets})
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{tagChange("aws_s3_bucket.logs", tfjson.Actions{tfjson.ActionUpdate}, "prod", "dev")}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{tagChange("aws_s3_bucket.logs", tfjson.Actions{tfjson.ActionUpdate}, "dev", "prod")}}
	edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{noChange("aws_s3_bucket.logs")}}
	var calls []string
	root := &fakeDriftTerraform{name: "root", calls: &calls, state: driftState}
	copied := &fakeDriftTerraform{name: "staged", calls: &calls, plans: []*tfjson.Plan{refresh, code, edited}, unformatted: []string{"main.tf"}}
	result, err := Drift(context.Background(), root, dir, staging, func(string) (DriftTerraform, error) { return copied, nil }, DriftOptions{Accept: func(DriftChange) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	if c := outcomes(result)["aws_s3_bucket.logs tags.Env"]; c.Outcome != DriftRejectedByCheck || !strings.Contains(c.Reason, CheckFormat) {
		t.Errorf("logs: %s: %s", c.Outcome, c.Reason)
	}
	if got := readFile(t, dir, "main.tf"); got != taggedBuckets {
		t.Errorf("main.tf changed:\n%s", got)
	}
}

// A check the edited code fails rejects only the changes whose edits are
// in the files it fails on: the others are planned, checked and written.
func TestDriftAcceptChecksBlameTheEditsOfTheFilesTheyFailOn(t *testing.T) {
	t.Parallel()
	const logs = `resource "aws_s3_bucket" "logs" {
  bucket = "logs"
  tags = {
    Env = "prod"
  }
}
`
	const data = `resource "aws_s3_bucket" "data" {
  bucket = "data"
  tags = {
    Env = "prod"
  }
}
`
	dir, staging := acceptRoot(t, map[string]string{"main.tf": logs, "data.tf": data})
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
		tagChange("aws_s3_bucket.logs", tfjson.Actions{tfjson.ActionUpdate}, "prod", "dev"),
		tagChange("aws_s3_bucket.data", tfjson.Actions{tfjson.ActionUpdate}, "prod", "dev"),
	}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		tagChange("aws_s3_bucket.logs", tfjson.Actions{tfjson.ActionUpdate}, "dev", "prod"),
		tagChange("aws_s3_bucket.data", tfjson.Actions{tfjson.ActionUpdate}, "dev", "prod"),
	}}
	both := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{noChange("aws_s3_bucket.logs"), noChange("aws_s3_bucket.data")}}
	logsOnly := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		noChange("aws_s3_bucket.logs"),
		tagChange("aws_s3_bucket.data", tfjson.Actions{tfjson.ActionUpdate}, "dev", "prod"),
	}}
	var calls []string
	root := &fakeDriftTerraform{name: "root", calls: &calls, state: driftState}
	copied := &fakeDriftTerraform{name: "staged", calls: &calls, plans: []*tfjson.Plan{refresh, code, both, logsOnly}, unformatted: []string{"data.tf"}}

	result, err := Drift(context.Background(), root, dir, staging, func(string) (DriftTerraform, error) { return copied, nil }, DriftOptions{Accept: func(DriftChange) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}

	got := outcomes(result)
	if c := got["aws_s3_bucket.data tags.Env"]; c.Outcome != DriftRejectedByCheck || !strings.Contains(c.Reason, "data.tf") {
		t.Errorf("data: %s: %s", c.Outcome, c.Reason)
	}
	if c := got["aws_s3_bucket.logs tags.Env"]; c.Outcome != DriftWritten {
		t.Errorf("logs: %s: %s", c.Outcome, c.Reason)
	}
	if got := readFile(t, dir, "data.tf"); got != data {
		t.Errorf("data.tf changed:\n%s", got)
	}
	if got := readFile(t, dir, "main.tf"); !strings.Contains(got, `Env = "dev"`) {
		t.Errorf("main.tf:\n%s", got)
	}
}

// Changes accepted that take a hand edit, or are secrets, aren't planned,
// and those kept stay kept.
func TestDriftAcceptDecides(t *testing.T) {
	t.Parallel()
	const main = `resource "aws_s3_bucket" "logs" {
  bucket = lower("LOGS")
  tags = {
    Env = "prod"
  }
}

resource "aws_db_instance" "db" {
  password = var.db_password
}
`
	const variables = `variable "db_password" {
  type      = string
  sensitive = true
}
`
	// The variable has a value, so that the password's change is assessed.
	dir, staging := acceptRoot(t, map[string]string{"main.tf": main, "variables.tf": variables, "terraform.tfvars": "db_password = \"from-tfvars\"\n"})
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{
		driftResource("aws_s3_bucket.logs", "aws_s3_bucket", tfjson.Actions{tfjson.ActionUpdate},
			map[string]any{"bucket": "logs", "tags": map[string]any{"Env": "prod"}},
			map[string]any{"bucket": "other", "tags": map[string]any{"Env": "dev"}}),
		sensitiveChange(driftResource("aws_db_instance.db", "aws_db_instance", tfjson.Actions{tfjson.ActionUpdate},
			map[string]any{"password": "old-secret-value"},
			map[string]any{"password": "new-secret-value"}), map[string]any{"password": true}),
	}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		driftResource("aws_s3_bucket.logs", "aws_s3_bucket", tfjson.Actions{tfjson.ActionUpdate},
			map[string]any{"bucket": "other", "tags": map[string]any{"Env": "dev"}},
			map[string]any{"bucket": "logs", "tags": map[string]any{"Env": "prod"}}),
		sensitiveChange(driftResource("aws_db_instance.db", "aws_db_instance", tfjson.Actions{tfjson.ActionUpdate},
			map[string]any{"password": "new-secret-value"},
			map[string]any{"password": "old-secret-value"}), map[string]any{"password": true}),
	}}
	var calls []string
	root := &fakeDriftTerraform{name: "root", calls: &calls, state: driftState}
	copied := &fakeDriftTerraform{name: "staged", calls: &calls, plans: []*tfjson.Plan{refresh, code}}
	accept := func(c DriftChange) bool { return c.Attribute != "tags.Env" }
	result, err := Drift(context.Background(), root, dir, staging, func(string) (DriftTerraform, error) { return copied, nil }, DriftOptions{Accept: accept})
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(result)
	for key, want := range map[string][2]string{
		"aws_s3_bucket.logs bucket":   {DriftAccept, DriftManual},
		"aws_s3_bucket.logs tags.Env": {DriftKeep, DriftKept},
		"aws_db_instance.db password": {DriftAccept, DriftSecret},
	} {
		if c := got[key]; c.Decision != want[0] || c.Outcome != want[1] {
			t.Errorf("%s: %s, %s (%s), want %s, %s", key, c.Decision, c.Outcome, c.Reason, want[0], want[1])
		}
	}
	if c := got["aws_db_instance.db password"]; !strings.Contains(c.Reason, "TF_VAR_db_password") {
		t.Errorf("the secret's reason: %s", c.Reason)
	}
	if len(copied.planOpts) != 2 {
		t.Errorf("%d plans, want the refresh and the code's only", len(copied.planOpts))
	}
	if got := readFile(t, dir, "main.tf"); got != main {
		t.Errorf("main.tf changed:\n%s", got)
	}
}

func TestRecordedTagLeaves(t *testing.T) {
	t.Parallel()
	upd := tfjson.Actions{tfjson.ActionUpdate}
	dev := map[string]any{"Env": "dev"}
	prod := map[string]any{"Env": "prod"}
	labels := map[string]any{"env": "dev"}
	for _, tc := range []struct {
		name string
		rc   *tfjson.ResourceChange
		want map[string]bool
	}{
		{"own tags only", driftResource("aws_s3_bucket.a", "aws_s3_bucket", upd,
			map[string]any{"tags": dev, "tags_all": dev}, map[string]any{"tags": nil, "tags_all": dev}), map[string]bool{"tags.Env": true}},
		{"tags_all changes too", driftResource("aws_s3_bucket.a", "aws_s3_bucket", upd,
			map[string]any{"tags": dev, "tags_all": dev}, map[string]any{"tags": nil, "tags_all": prod}), map[string]bool{}},
		{"tags_all unknown", &tfjson.ResourceChange{Address: "aws_s3_bucket.a", Type: "aws_s3_bucket", Mode: tfjson.ManagedResourceMode, Change: &tfjson.Change{Actions: upd,
			Before: map[string]any{"tags": dev, "tags_all": dev}, After: map[string]any{"tags": nil}, AfterUnknown: map[string]any{"tags_all": true}}}, map[string]bool{}},
		{"no tags_all", driftResource("azurerm_resource_group.a", "azurerm_resource_group", upd,
			map[string]any{"tags": dev}, map[string]any{"tags": prod}), map[string]bool{}},
		{"labels", driftResource("google_storage_bucket.a", "google_storage_bucket", upd,
			map[string]any{"labels": nil, "terraform_labels": labels, "effective_labels": labels},
			map[string]any{"labels": labels, "terraform_labels": labels, "effective_labels": labels}), map[string]bool{"labels.env": true}},
	} {
		if got := recordedTagLeaves(tc.rc); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The standards check of the edited code leaves the organisation's own
// modules to their owners, as the verification gate does: it doesn't
// read them, even where they hold what it couldn't parse.
func TestDriftAcceptLeavesOwnModulesToTheirOwners(t *testing.T) {
	t.Parallel()
	dir, staging := acceptRoot(t, map[string]string{"main.tf": taggedBuckets + `
module "own" {
  source = "../../../own"
}
`})
	own := filepath.Join(dir, "..", "..", "..", "own")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "main.tf"), []byte("resource \"aws_sqs_queue\" \"this\" {\n  name = \"${\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refresh := &tfjson.Plan{ResourceDrift: []*tfjson.ResourceChange{tagChange("aws_s3_bucket.logs", tfjson.Actions{tfjson.ActionUpdate}, "prod", "dev")}}
	code := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{tagChange("aws_s3_bucket.logs", tfjson.Actions{tfjson.ActionUpdate}, "dev", "prod")}}
	edited := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{noChange("aws_s3_bucket.logs")}}
	var calls []string
	root := &fakeDriftTerraform{name: "root", calls: &calls, state: driftState}
	copied := &fakeDriftTerraform{name: "staged", calls: &calls, plans: []*tfjson.Plan{refresh, code, edited}}
	opts := DriftOptions{Accept: func(DriftChange) bool { return true }, Adapters: []adapters.Adapter{{Source: own}}}

	result, err := Drift(context.Background(), root, dir, staging, func(string) (DriftTerraform, error) { return copied, nil }, opts)
	if err != nil {
		t.Fatal(err)
	}

	if c := outcomes(result)["aws_s3_bucket.logs tags.Env"]; c.Outcome != DriftWritten {
		t.Errorf("logs: %s: %s", c.Outcome, c.Reason)
	}
}
