// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/managed"
	"github.com/IgnatG/infraharvest/internal/report"
)

// writeAdoptable writes, in out/aws/eu-west-2, a root dev and prod share
// as a merge leaves it, with dev's imports.tfvars and prod's backend.hcl:
// prod's imports.tfvars is deleted, as after its first apply. It returns
// the root's directory.
func writeAdoptable(t *testing.T, out string) string {
	t.Helper()
	stack := filepath.Join(out, "aws", "eu-west-2")
	writeRootFiles(t, stack, map[string]string{"providers.tf": "# providers\n", "sqs.tf": "# queues\n", "README.md": "# aws/eu-west-2\n"})
	writeRootFiles(t, filepath.Join(stack, "vars"), map[string]string{"common.tfvars": "# none\n"})
	writeRootFiles(t, filepath.Join(stack, "vars", "dev"), map[string]string{
		"terraform.tfvars": "env = \"dev\"\n",
		"imports.tfvars":   "import_ids = {\n  \"aws_sqs_queue.orders\" = \"q-dev-orders\"\n}\n",
	})
	writeRootFiles(t, filepath.Join(stack, "vars", "prod"), map[string]string{
		"terraform.tfvars": "env = \"prd\"\n",
		"backend.hcl":      "bucket = \"acme-state\"\nkey = \"envs/prod/aws/222222222222/eu-west-2/terraform.tfstate\"\nregion = \"eu-west-2\"\n",
	})
	return stack
}

func TestMergedRootDirs(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	stack := writeAdoptable(t, out)
	writeRoot(t, filepath.Join(out, "aws", "111111111111", "global"), "iam.tf")
	// Under the checkpoints, a root set aside isn't one.
	writeAdoptable(t, filepath.Join(out, CheckpointDir, "premerge"))
	dirs, err := mergedRootDirs(out)
	if err != nil || !reflect.DeepEqual(dirs, []string{stack}) {
		t.Errorf("got %v, %v, want %s", dirs, err, stack)
	}
	if dirs, err := mergedRootDirs(filepath.Join(out, "missing")); err != nil || len(dirs) != 0 {
		t.Errorf("a missing output: %v %v", dirs, err)
	}
}

// The states of the environments of a merged root are that root's: each
// environment's workspace, and the state its backend.hcl names.
func TestMergedOwnStates(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	stack := writeAdoptable(t, out)
	own, err := mergedOwnStates(out, true)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]mergedState{
		stateLocation(filepath.Join(stack, "terraform.tfstate.d", "dev", "terraform.tfstate")):  {stack, "dev"},
		stateLocation(filepath.Join(stack, "terraform.tfstate.d", "prod", "terraform.tfstate")): {stack, "prod"},
		"s3://acme-state/envs/prod/aws/222222222222/eu-west-2/terraform.tfstate":                {stack, "prod"},
	}
	if !reflect.DeepEqual(own, want) {
		t.Errorf("got %v, want %v", own, want)
	}
	// Once a merge checkpoint records it, the import adds to the roots it
	// kept instead, unless asked for every merged root.
	if err := writeMergeCheckpoint(out, stack, mergeCheckpoint{Merge: report.Merge{Merged: true}}); err != nil {
		t.Fatal(err)
	}
	if own, err := mergedOwnStates(out, true); err != nil || len(own) != 0 {
		t.Errorf("recorded: %v %v", own, err)
	}
	if own, err := mergedOwnStates(out, false); err != nil || !reflect.DeepEqual(own, want) {
		t.Errorf("every merged root: %v %v", own, err)
	}
}

// With --incremental, what the state of an environment of a merged root
// that no checkpoint records has isn't left out as another state's: its
// stack merges again from the environments' roots generated with it.
// Where a checkpoint records the merge, the import adds to the roots it
// kept, and leaves it out; with --regenerate it is the root's again.
func TestExcludeManagedKeepsMergedRootsOwn(t *testing.T) {
	out := t.TempDir()
	stack := writeAdoptable(t, out)
	workspace := filepath.Join(stack, "terraform.tfstate.d", "dev")
	writeRootFiles(t, workspace, map[string]string{"terraform.tfstate": `{"version": 4, "resources": [{"mode": "managed", "type": "aws_s3_bucket", "name": "archive", "instances": [{"attributes": {"id": "old-archive"}}]}]}`})
	other := filepath.Join(t.TempDir(), "network.tfstate")
	if err := os.WriteFile(other, []byte(`{"version": 4, "resources": [{"mode": "managed", "type": "aws_vpc", "name": "main", "instances": [{"attributes": {"id": "vpc-0abc1234"}}]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sources := []string{filepath.Join(stack, "terraform.tfstate.d"), other}
	check := func(name string, options Options, bucketManaged bool) {
		t.Helper()
		defaults, err := excludeManaged(t.Context(), NewRun(), options, listedForSelection, nil, importIDForSelection)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, got := defaults["aws_s3_bucket old-archive"]; got != bucketManaged {
			t.Errorf("%s: the bucket of dev's state left out %v, want %v", name, got, bucketManaged)
		}
		if _, got := defaults["aws_vpc vpc-0abc1234"]; !got {
			t.Errorf("%s: the VPC of another state isn't left out", name)
		}
	}
	check("incremental", Options{ManagedState: sources, Out: out, Incremental: true}, false)
	check("a plain import", Options{ManagedState: sources, Out: out}, true)
	if err := writeMergeCheckpoint(out, stack, mergeCheckpoint{Merge: report.Merge{Merged: true}}); err != nil {
		t.Fatal(err)
	}
	check("incremental, recorded", Options{ManagedState: sources, Out: out, Incremental: true}, true)
	check("regenerate, recorded", Options{ManagedState: sources, Out: out, Regenerate: true}, false)
}

// A merged root no checkpoint records is told from its files: its
// environments, its files, and what it has of each environment, from
// imports.tfvars, or, once that is deleted, from the environment's own
// state.
func TestAdoptedCheckpoint(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	stack := writeAdoptable(t, out)
	dev := mergeMember{env: "dev", dir: filepath.Join(out, "envs", "dev", "aws", "111111111111", "eu-west-2"), result: &engine.Result{
		Addresses: []engine.Managed{{Type: "aws_sqs_queue", ID: "q-dev-orders", Address: "aws_sqs_queue.shop_dev_orders"}},
	}}
	prod := mergeMember{env: "prod", dir: filepath.Join(out, "envs", "prod", "aws", "222222222222", "eu-west-2"), result: &engine.Result{
		Addresses: []engine.Managed{
			{Type: "aws_sqs_queue", ID: "q-prd-orders", Address: "aws_sqs_queue.shop_prd_orders"},
			{Type: "aws_sqs_queue", ID: "q-prd-billing", Address: "aws_sqs_queue.shop_prd_billing"},
		},
	}}
	run := NewRun()
	run.options = Options{Out: out, Incremental: true}

	// Without prod's imports.tfvars or state, what it has can't be told.
	c, why, err := run.adoptedCheckpoint(stack, []mergeMember{dev, prod})
	if err != nil || c == nil || !strings.Contains(why, "says what the merged root has of prod") {
		t.Errorf("without prod's state: %+v, %q, %v", c, why, err)
	}

	state := managed.Resources{}
	content := `{"version": 4, "resources": [
	  {"mode": "managed", "type": "aws_sqs_queue", "name": "orders", "instances": [{"attributes": {"id": "q-prd-orders"}}]},
	  {"mode": "managed", "type": "aws_sqs_queue", "name": "other", "instances": [{"attributes": {"id": "q-elsewhere"}}]}
	]}`
	if err := state.Parse([]byte(content), "s3://acme-state/envs/prod/aws/222222222222/eu-west-2/terraform.tfstate"); err != nil {
		t.Fatal(err)
	}
	run.options.ManagedState = []string{"backend"}
	run.shared.managed = map[string]managed.Resources{"s3://acme-state/?region=eu-west-2": state}
	c, why, err = run.adoptedCheckpoint(stack, []mergeMember{dev, prod})
	if err != nil || why != "" {
		t.Fatalf("%q, %v", why, err)
	}
	if !c.Merge.Merged || c.Merge.Path != "aws/eu-west-2" || strings.Join(c.Merge.Environments, ",") != "dev,prod" {
		t.Errorf("merge: %+v", c.Merge)
	}
	wantImports := map[string]map[string]string{
		"dev":  {"aws_sqs_queue.orders": "q-dev-orders"},
		"prod": {"aws_sqs_queue.orders": "q-prd-orders"},
	}
	if !reflect.DeepEqual(c.Imports, wantImports) {
		t.Errorf("imports: %v, want %v", c.Imports, wantImports)
	}
	wantFiles := []string{"README.md", "providers.tf", "sqs.tf", "vars/common.tfvars", "vars/dev/imports.tfvars", "vars/dev/terraform.tfvars", "vars/prod/backend.hcl", "vars/prod/terraform.tfvars"}
	if !reflect.DeepEqual(c.Files, wantFiles) {
		t.Errorf("files: %v, want %v", c.Files, wantFiles)
	}
	if len(c.Members) != 2 || c.Members[1].Path != "envs/prod/aws/222222222222/eu-west-2" {
		t.Errorf("members: %+v", c.Members)
	}
	if got := newSinceMerge(prod.result, c.Imports["prod"]); !reflect.DeepEqual(got, []string{"aws_sqs_queue.shop_prd_billing"}) {
		t.Errorf("new since the merge: %v", got)
	}

	// An import that lacks one of its environments can't merge it again.
	if _, why, err := run.adoptedCheckpoint(stack, []mergeMember{dev}); err != nil || !strings.Contains(why, "doesn't have prod") {
		t.Errorf("without prod: %q, %v", why, err)
	}
	// A directory that isn't a merged root isn't adopted.
	if c, why, err := run.adoptedCheckpoint(filepath.Join(out, "aws", "us-east-1"), []mergeMember{dev, prod}); c != nil || why != "" || err != nil {
		t.Errorf("no merged root: %+v, %q, %v", c, why, err)
	}
}

// An incremental import from a fresh checkout that lacks one of the
// environments of a merged root keeps the merged root as it was, sets
// the root it generated aside, and fails, saying why.
func TestMergeStackAdoptedWithoutAnEnvironment(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	stack := writeAdoptable(t, out)
	dir := filepath.Join(out, "envs", "dev", "aws", "111111111111", "eu-west-2")
	writeRootFiles(t, dir, map[string]string{engine.VersionsFileName: "# versions\n", "sqs.tf": "# queues\n"})
	run := NewRun()
	run.options = Options{Out: out, Incremental: true}
	dev := mergeMember{env: "dev", dir: dir, stack: stack, result: &engine.Result{}}
	if err := run.mergeStack(context.Background(), stack, []mergeMember{dev}); err != nil {
		t.Fatal(err)
	}
	if exists(dir) || !exists(filepath.Join(premergeDir(out, dir), "sqs.tf")) {
		t.Errorf("dev's root isn't set aside")
	}
	if !exists(filepath.Join(stack, "sqs.tf")) {
		t.Errorf("the merged root isn't kept")
	}
	if len(run.report.Failures) != 1 || !strings.Contains(run.report.Failures[0], "doesn't have prod") {
		t.Errorf("failures: %v", run.report.Failures)
	}
}

func TestBlockAddress(t *testing.T) {
	t.Parallel()
	for address, want := range map[string]string{
		"aws_sqs_queue.orders":                  "aws_sqs_queue.orders",
		`aws_sqs_queue.this["orders"]`:          "aws_sqs_queue.this",
		"module.logs.aws_s3_bucket.this[0]":     "module.logs",
		`module.queues["a"].aws_sqs_queue.this`: "module.queues",
	} {
		if got := blockAddress(address); got != want {
			t.Errorf("blockAddress(%q) = %q, want %q", address, got, want)
		}
	}
}

// A module call is new since the merge only where none of its resources
// was there.
func TestNewSinceMerge(t *testing.T) {
	t.Parallel()
	result := &engine.Result{Addresses: []engine.Managed{
		{Type: "aws_s3_bucket", ID: "logs", Address: "module.logs.aws_s3_bucket.this"},
		{Type: "aws_s3_bucket_versioning", ID: "logs", Address: "module.logs.aws_s3_bucket_versioning.this[0]"},
		{Type: "aws_s3_bucket", ID: "audit", Address: "module.audit.aws_s3_bucket.this"},
		{Type: "aws_sqs_queue", ID: "q1", Address: "aws_sqs_queue.orders"},
	}}
	got := newSinceMerge(result, map[string]string{"module.logs.aws_s3_bucket.this": "logs", "aws_sqs_queue.orders": "q1"})
	if !reflect.DeepEqual(got, []string{"module.audit"}) {
		t.Errorf("got %v", got)
	}
}

func TestLeftOutOfMerge(t *testing.T) {
	t.Parallel()
	before := map[string]map[string]string{"dev": {"aws_sqs_queue.orders": "q1", "aws_sqs_queue.audit[0]": "q2"}, "prod": {"aws_sqs_queue.orders": "q3"}}
	now := map[string]map[string]string{"dev": {`aws_sqs_queue.this["orders"]`: "q1"}, "prod": {"aws_sqs_queue.orders": "q3", "aws_sqs_queue.new[0]": "q4"}}
	if got := leftOutOfMerge(before, now); !reflect.DeepEqual(got, []string{"dev: aws_sqs_queue.audit[0]"}) {
		t.Errorf("got %v", got)
	}
}

// What a merge from a root's files would give another address, an earlier
// merge kept apart: its blocks are kept apart again, once.
func TestMovedApart(t *testing.T) {
	t.Parallel()
	m := mergeMember{env: "dev", isolated: []string{"aws_sqs_queue.shop_dev_billing"}, result: &engine.Result{Addresses: []engine.Managed{
		{Type: "aws_sqs_queue", ID: "q-audit", Address: "aws_sqs_queue.shop_dev_audit"},
		{Type: "aws_sqs_queue", ID: "q-reports", Address: "aws_sqs_queue.shop_dev_reports"},
		{Type: "aws_sqs_queue", ID: "q-orders", Address: "aws_sqs_queue.shop_dev_orders"},
		{Type: "aws_sqs_queue", ID: "q-billing", Address: "aws_sqs_queue.shop_dev_billing"},
	}}}
	before := map[string]string{"aws_sqs_queue.audit[0]": "q-audit", "aws_sqs_queue.reports[0]": "q-reports", "aws_sqs_queue.orders": "q-orders"}
	now := map[string]string{`aws_sqs_queue.this["audit"]`: "q-audit", `aws_sqs_queue.this["reports"]`: "q-reports", "aws_sqs_queue.orders": "q-orders", "aws_sqs_queue.billing[0]": "q-billing"}
	if got := movedApart(m, before, now, nil); !reflect.DeepEqual(got, map[string]string{"aws_sqs_queue.shop_dev_audit": "audit", "aws_sqs_queue.shop_dev_reports": "reports"}) {
		t.Errorf("got %v", got)
	}
	// One the merged root matched with other environments' stays merged.
	if got := movedApart(m, before, now, map[string]bool{"aws_sqs_queue.audit[0]": true}); !reflect.DeepEqual(got, map[string]string{"aws_sqs_queue.shop_dev_reports": "reports"}) {
		t.Errorf("shared: got %v", got)
	}
	m.isolated = append(m.isolated, "aws_sqs_queue.shop_dev_audit", "aws_sqs_queue.shop_dev_reports")
	if got := movedApart(m, before, now, nil); len(got) != 0 {
		t.Errorf("kept apart already: %v", got)
	}
}

func TestMergedLabel(t *testing.T) {
	t.Parallel()
	for address, want := range map[string]string{
		"aws_sqs_queue.shop_audit[0]":       "shop_audit",
		`aws_sqs_queue.this["orders"]`:      "this",
		"module.logs[0].aws_s3_bucket.this": "logs",
		"aws_sqs_queue.orders":              "orders",
	} {
		if got := mergedLabel(address); got != want {
			t.Errorf("mergedLabel(%q) = %q, want %q", address, got, want)
		}
	}
}

func TestSharedAddresses(t *testing.T) {
	t.Parallel()
	got := sharedAddresses(map[string]map[string]string{
		"dev":  {"google_storage_bucket.main": "dev-orders", "google_pubsub_topic.main[0]": "dev-events"},
		"prod": {"google_storage_bucket.main": "prd-orders"},
	})
	if !reflect.DeepEqual(got, map[string]bool{"google_storage_bucket.main": true}) {
		t.Errorf("got %v", got)
	}
}
