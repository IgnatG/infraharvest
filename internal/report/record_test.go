// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"errors"
	"reflect"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/managed"
)

// Each kind of exclusion gives its reason and owner, and Finish counts
// the excluded resources by what manages them.
func TestExclude(t *testing.T) {
	t.Parallel()
	scope := "aws/1/eu-west-2"
	for _, tc := range []struct {
		why    Why
		reason string
		owner  string
	}{
		{InRoot(), "already in the root", OwnerTerraformRoot},
		{InOtherRoot("aws/1/global"), "already in another root of the output, aws/1/global", OwnerTerraformRoot},
		{Decided(managed.Reason + " (s3://state)"), managed.Reason + " (s3://state)", OwnerTerraformState},
		{Decided(OtherToolPrefix + "CloudFormation stack app"), OtherToolPrefix + "CloudFormation stack app", OwnerCloudFormation},
		{Decided("a default VPC"), "a default VPC", OwnerCloud},
		{Decided(ReasonSelectionRule), ReasonSelectionRule, OwnerSelection},
		// What tags say keeps the owner it is given.
		{Owned(OtherToolPrefix+"CloudFormation (per its tag ManagedBy=cloudformation)", OwnerOtherTool), OtherToolPrefix + "CloudFormation (per its tag ManagedBy=cloudformation)", OwnerOtherTool},
	} {
		r := &Report{}
		r.Exclude(scope, "aws_sqs_queue", "q1", tc.why)
		want := []Excluded{{Type: "aws_sqs_queue", ID: "q1", Reason: tc.reason, Scope: scope, Owner: tc.owner}}
		if !reflect.DeepEqual(r.Excluded, want) {
			t.Errorf("got %+v, want %+v", r.Excluded, want)
		}
	}

	r := &Report{}
	r.AddDiscovered(scope, 6)
	r.Exclude(scope, "aws_sqs_queue", "q1", InRoot())
	r.Exclude(scope, "aws_sqs_queue", "q2", InOtherRoot("aws/1/global"))
	r.Exclude(scope, "aws_s3_bucket", "state", Decided(managed.Reason+" (s3://state)"))
	r.Exclude(scope, "aws_s3_bucket", "stack", Owned(OtherToolPrefix+"Pulumi (per its tag ManagedBy=pulumi)", OwnerOtherTool))
	r.Exclude(scope, "aws_vpc", "vpc-1", Decided("a default VPC"))
	r.Exclude(scope, "aws_vpc", "vpc-2", Decided(ReasonSelectionEntry))
	r.Finish(map[string]int{"aws_sqs_queue": 2, "aws_s3_bucket": 2, "aws_vpc": 2}, nil, false)
	want := CoverageTotal{Discovered: 6, Excluded: 6, Managed: 3, OtherTool: 1, Cloud: 1}
	if r.Totals != want {
		t.Errorf("totals %+v, want %+v", r.Totals, want)
	}
	if got := r.Scopes[0].CoverageTotal; got != want {
		t.Errorf("scope's totals %+v, want %+v", got, want)
	}
}

// A directory has each resource at its address in the root: in the module
// call Generate moved it into, not at the label Generate gave it, and at
// that label for a result without addresses, as an older release's.
func TestAddDirectoryAddresses(t *testing.T) {
	t.Parallel()
	r := &Report{}
	imported := []engine.Import{{Type: "aws_iam_role", Name: "app", ID: "app"}, {Type: "aws_iam_role", Name: "web", ID: "web"}}
	r.AddDirectory("aws", "aws/1/global", &engine.Result{Imported: imported, Addresses: []engine.Managed{
		{Type: "aws_iam_role", ID: "app", Address: "module.app.aws_iam_role.this"},
		{Type: "aws_iam_role", ID: "web", Address: "aws_iam_role.web"},
	}})
	r.AddDirectory("older", "", &engine.Result{Imported: imported[:1]})
	r.AddFailedDirectory("failed", "aws/1/eu-west-2", errors.New("no credentials"))

	want := []Directory{
		{Path: "aws", Scope: "aws/1/global", Imported: []Resource{{Address: "module.app.aws_iam_role.this", ID: "app"}, {Address: "aws_iam_role.web", ID: "web"}}},
		{Path: "older", Imported: []Resource{{Address: "aws_iam_role.app", ID: "app"}}},
		{Path: "failed", Scope: "aws/1/eu-west-2", Error: "no credentials"},
	}
	if !reflect.DeepEqual(r.Directories, want) {
		t.Errorf("got %+v, want %+v", r.Directories, want)
	}
}

// A directory has what Generate left out, its module calls, secrets,
// warnings and checks, which Finish counts.
func TestAddDirectoryOutcome(t *testing.T) {
	t.Parallel()
	r := &Report{}
	r.AddDirectory("aws", "", &engine.Result{
		Imported: []engine.Import{{Type: "aws_sqs_queue", Name: "jobs", ID: "q1"}},
		Rejected: []engine.Rejection{{Address: "aws_lb.web", ID: "arn:lb", Errors: []string{"invalid"}}},
		Modules:  []engine.ModuleCall{{Name: "jobs", Source: "terraform-aws-modules/sqs/aws", Version: "5.0.0", Resources: []string{"aws_sqs_queue.jobs"}}},
		Secrets:  []engine.Secret{{Variable: "token", Address: "aws_ssm_parameter.token", Attribute: "value"}},
		Warnings: []string{"a tag named after an access key"},
		Gate:     engine.Gate{{Name: "valid", Passed: false, Details: []string{"bad"}}},
	})
	d := r.Directories[0]
	if !reflect.DeepEqual(d.LeftOut, []LeftOut{{Address: "aws_lb.web", ID: "arn:lb", Errors: []string{"invalid"}}}) ||
		len(d.Modules) != 1 || d.Modules[0].Name != "jobs" ||
		!reflect.DeepEqual(d.Secrets, []Secret{{Variable: "token", Address: "aws_ssm_parameter.token", Attribute: "value"}}) ||
		!reflect.DeepEqual(d.Warnings, []string{"a tag named after an access key"}) ||
		!reflect.DeepEqual(d.Checks, []Check{{Name: "valid", Details: []string{"bad"}}}) {
		t.Errorf("directory %+v", d)
	}
	r.Finish(map[string]int{"aws_sqs_queue": 1, "aws_lb": 1}, nil, false)
	if want := (CoverageTotal{Discovered: 2, Imported: 1, LeftOut: 1}); r.Totals != want {
		t.Errorf("totals %+v, want %+v", r.Totals, want)
	}
}

// A directory's moves are recorded with it: a move into a module call the
// result has no address for keeps its resource's type, and what a root
// imports from another is the address the other's departure goes to.
func TestAddDirectoryMoves(t *testing.T) {
	t.Parallel()
	r := &Report{}
	r.AddDirectory("aws/eu-west-2", "", &engine.Result{
		Moves: []engine.Move{{From: "aws_s3_bucket.logs", To: `module.logs["a.b"].aws_s3_bucket.this`}},
		Removed: []engine.Removal{{Resources: []engine.Departure{
			{Type: "aws_sqs_queue", ID: "q1", Address: "aws_sqs_queue.jobs", Root: "aws/global"},
		}}},
	})
	r.AddDirectory("aws/global", "", &engine.Result{
		Arrived: []engine.Arrival{{Type: "aws_sqs_queue", ID: "q1", Root: "aws/eu-west-2", From: "aws_sqs_queue.jobs", To: "aws_sqs_queue.orders"}},
	})
	r.Finish(nil, nil, false)
	if len(r.Moved) != 1 || r.Moved[0].Type != "aws_s3_bucket" {
		t.Errorf("moved: %+v", r.Moved)
	}
	want := []Move{{Type: "aws_sqs_queue", ID: "q1", FromRoot: "aws/eu-west-2", From: "aws_sqs_queue.jobs", ToRoot: "aws/global", To: "aws_sqs_queue.orders"}}
	if !reflect.DeepEqual(r.Departed, want) {
		t.Errorf("departed %+v, want %+v", r.Departed, want)
	}
}

// A merged root merged again moves what has another address now, and
// hands over what its removed blocks do.
func TestAddMergedMoves(t *testing.T) {
	t.Parallel()
	r := &Report{}
	before := map[string]map[string]string{"dev": {"aws_sqs_queue.a": "q1", "aws_sqs_queue.b": "q2"}}
	now := map[string]map[string]string{"dev": {"aws_sqs_queue.orders": "q1", "aws_sqs_queue.b": "q2"}}
	removed := []engine.Removal{{Resources: []engine.Departure{{Type: "aws_sqs_queue", ID: "q3", Address: "aws_sqs_queue.c", Root: "aws/dev"}}}}
	r.AddMergedMoves("aws", before, now, removed)
	if want := []Move{{Type: "aws_sqs_queue", ID: "q1", FromRoot: "aws", From: "aws_sqs_queue.a", ToRoot: "aws", To: "aws_sqs_queue.orders"}}; !reflect.DeepEqual(r.Moved, want) {
		t.Errorf("moved %+v, want %+v", r.Moved, want)
	}
	if want := []Move{{Type: "aws_sqs_queue", ID: "q3", FromRoot: "aws", From: "aws_sqs_queue.c", ToRoot: "aws/dev"}}; !reflect.DeepEqual(r.Departed, want) {
		t.Errorf("departed %+v, want %+v", r.Departed, want)
	}
}

// What a root has that is no longer in the cloud is the merged root's,
// at its address there, once the root's stack merges.
func TestReaddressNotInCloud(t *testing.T) {
	t.Parallel()
	r := &Report{}
	r.AddNotInCloud("aws/dev", "aws/1/eu-west-2", []engine.Import{{Type: "aws_sqs_queue", Name: "orders", ID: "q1"}, {Type: "aws_sqs_queue", Name: "audit", ID: "q3"}})
	if got := r.NotInCloudAt("aws/dev"); !reflect.DeepEqual(got, []string{"aws_sqs_queue.audit", "aws_sqs_queue.orders"}) {
		t.Errorf("gone %v", got)
	}
	gone := r.ReaddressNotInCloud("aws", map[string]string{"aws/dev": "dev"}, map[string]map[string]string{"dev": {"aws_sqs_queue.orders": "Q1", "aws_sqs_queue.audit[0]": "q3"}})
	if want := map[string][]engine.Import{"dev": {{Type: "aws_sqs_queue", Name: "orders", ID: "q1"}, {Type: "aws_sqs_queue", Name: "audit[0]", ID: "q3"}}}; !reflect.DeepEqual(gone, want) {
		t.Errorf("readdressed %+v, want %+v", gone, want)
	}
	if got := r.NotInCloudAt("aws/dev"); len(got) != 0 {
		t.Errorf("gone from the root once merged: %v", got)
	}
	want := []NotInCloud{
		{Path: "aws", Address: "aws_sqs_queue.orders", ID: "q1", Scope: "aws/1/eu-west-2", Environment: "dev"},
		{Path: "aws", Address: "aws_sqs_queue.audit[0]", ID: "q3", Scope: "aws/1/eu-west-2", Environment: "dev"},
	}
	if !reflect.DeepEqual(r.NotInCloud, want) {
		t.Errorf("not in the cloud %+v, want %+v", r.NotInCloud, want)
	}
}

// A merged stack's roots name the merged root; those kept apart are on
// the stack's merge.
func TestAddMerge(t *testing.T) {
	t.Parallel()
	r := &Report{}
	r.AddDirectory("aws/dev", "", &engine.Result{})
	r.AddDirectory("aws/prod", "", &engine.Result{})
	r.AddDirectory("aws/test", "", &engine.Result{})
	r.AddMerge(Merge{Path: "aws", Merged: true, Environments: []string{"dev", "prod"}}, []string{"aws/dev", "aws/prod"})
	r.AddMerge(Merge{Path: "gcp", Reason: "too different"}, []string{"aws/test"})
	r.SetApart("aws", []MergedRoot{{Env: "test", Path: "aws/test"}}, "joining failed")
	for _, d := range r.Directories {
		if want := map[string]string{"aws/dev": "aws", "aws/prod": "aws"}[d.Path]; d.MergedInto != want {
			t.Errorf("%s merged into %q, want %q", d.Path, d.MergedInto, want)
		}
	}
	if m := r.Merges[0]; len(m.Apart) != 1 || m.ApartReason != "joining failed" {
		t.Errorf("merge %+v", m)
	}
	if m := r.Merges[1]; m.Apart != nil {
		t.Errorf("another stack's merge %+v", m)
	}
}

func TestAddWarning(t *testing.T) {
	t.Parallel()
	r := &Report{}
	if !r.AddWarning("w") || r.AddWarning("w") || !reflect.DeepEqual(r.Warnings, []string{"w"}) {
		t.Errorf("warnings %v", r.Warnings)
	}
}
