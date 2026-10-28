// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
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
		{Decided(TerraformPrefix + " (s3://state)"), TerraformPrefix + " (s3://state)", OwnerTerraformState},
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
	r.Exclude(scope, "aws_s3_bucket", "state", Decided(TerraformPrefix+" (s3://state)"))
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
		Secrets:  []secret.Variable{{Variable: "token", Address: "aws_ssm_parameter.token", Attribute: "value"}},
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

// rendered returns r as its JSON report, decoded, and as report.md.
func rendered(t *testing.T, r *Report) (map[string]any, string) {
	t.Helper()
	var out bytes.Buffer
	if err := r.WriteJSON(&out); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, out.String())
	}
	return doc, r.Markdown()
}

// Failures are recorded as their messages, sorted by Finish, make the
// import incomplete, and are listed in the JSON report and report.md.
func TestAddFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		allowPartial bool
		exitCode     int
	}{
		{"incomplete", false, ExitIncomplete},
		{"partial", true, ExitPartial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &Report{}
			r.AddFailure(errors.New("service s3: access denied"))
			r.AddFailure(fmt.Errorf("account %s: %w", "111122223333", errors.New("assume role: access denied")))
			r.Finish(nil, nil, tc.allowPartial)

			want := []string{"account 111122223333: assume role: access denied", "service s3: access denied"}
			if !reflect.DeepEqual(r.Failures, want) {
				t.Errorf("failures %q, want %q", r.Failures, want)
			}
			if r.ExitCode != tc.exitCode {
				t.Errorf("exit code %d, want %d", r.ExitCode, tc.exitCode)
			}
			doc, md := rendered(t, r)
			if got := doc["failures"]; !reflect.DeepEqual(got, []any{want[0], want[1]}) {
				t.Errorf("JSON failures %v, want %q", got, want)
			}
			if !strings.Contains(md, "\n## Failures\n") || !strings.Contains(md, "\n- "+want[0]+"\n- "+want[1]+"\n") {
				t.Errorf("report.md:\n%s", md)
			}
		})
	}
	if doc, md := rendered(t, &Report{}); doc["failures"] != nil || strings.Contains(md, "## Failures") {
		t.Errorf("a report without failures lists them: %v\n%s", doc["failures"], md)
	}
}

// Types Terraform can't import are counted, with the reason, as not
// importable, sorted by type, without making the import incomplete.
func TestAddSkipped(t *testing.T) {
	t.Parallel()
	r := &Report{}
	r.AddSkipped("aws_main_route_table_association", 2)
	r.AddSkipped("aws_default_network_acl", 1)
	r.Finish(map[string]int{"aws_main_route_table_association": 2, "aws_default_network_acl": 1}, nil, false)

	want := []Skipped{
		{Type: "aws_default_network_acl", Count: 1, Reason: skippedReason},
		{Type: "aws_main_route_table_association", Count: 2, Reason: skippedReason},
	}
	if !reflect.DeepEqual(r.Skipped, want) {
		t.Errorf("skipped %+v, want %+v", r.Skipped, want)
	}
	if r.Totals.Skipped != 3 || r.Totals.Discovered != 3 || r.ExitCode != ExitOK {
		t.Errorf("totals %+v, exit code %d; want 3 not importable and a complete import", r.Totals, r.ExitCode)
	}
	doc, md := rendered(t, r)
	skipped, _ := doc["skipped"].([]any)
	if len(skipped) != 2 || !reflect.DeepEqual(skipped[1], map[string]any{"type": "aws_main_route_table_association", "count": 2.0, "reason": skippedReason}) {
		t.Errorf("JSON skipped %v", doc["skipped"])
	}
	for _, line := range []string{
		"\n## Not importable\n",
		"- `aws_default_network_acl` (1): " + skippedReason + "\n- `aws_main_route_table_association` (2): " + skippedReason + "\n",
		"| `aws_main_route_table_association` | 2 | 0 | 0 | 0 | 2 | 0 |",
	} {
		if !strings.Contains(md, line) {
			t.Errorf("report.md misses %q:\n%s", line, md)
		}
	}
}

// Kept roots are added to those there are, nil adding none, and are in the
// JSON report.
func TestAddKeptRoots(t *testing.T) {
	t.Parallel()
	r := &Report{}
	r.AddKeptRoots(nil)
	if doc, md := rendered(t, r); doc["kept_roots"] != nil || strings.Contains(md, "Kept where they were") {
		t.Errorf("no kept roots, yet listed: %v\n%s", doc["kept_roots"], md)
	}
	prod := KeptRoot{Path: "aws/2/us-east-1", Environments: []string{"prod"}, Scope: "aws/2/us-east-1"}
	dev := KeptRoot{Path: "aws/1/us-east-1", Environments: []string{"dev"}}
	r.AddKeptRoots([]KeptRoot{prod})
	r.AddKeptRoots([]KeptRoot{dev})
	if want := []KeptRoot{prod, dev}; !reflect.DeepEqual(r.KeptRoots, want) {
		t.Errorf("kept roots %+v, want %+v", r.KeptRoots, want)
	}
	r.Finish(nil, nil, false)
	doc, md := rendered(t, r)
	want := []any{
		map[string]any{"path": "aws/1/us-east-1", "environments": []any{"dev"}},
		map[string]any{"path": "aws/2/us-east-1", "environments": []any{"prod"}, "scope": "aws/2/us-east-1"},
	}
	if !reflect.DeepEqual(doc["kept_roots"], want) {
		t.Errorf("JSON kept roots %v, want %v", doc["kept_roots"], want)
	}
	if !strings.Contains(md, "- `aws/1/us-east-1`, of dev\n- `aws/2/us-east-1`, of prod\n") {
		t.Errorf("report.md:\n%s", md)
	}
}

// Handovers one root didn't finish are sorted by the root and address
// they come from, said in report.md with what to do, and kept whole in
// the JSON report; they don't make the import incomplete themselves, as
// the failed root does.
func TestAddIncompleteHandover(t *testing.T) {
	t.Parallel()
	r := &Report{}
	queue := IncompleteHandover{
		Move:       Move{From: "aws_sqs_queue.orders", FromRoot: "aws/1/eu-west-2", To: "aws_sqs_queue.orders", ToRoot: "envs/prod/aws/1/eu-west-2"},
		FailedRoot: "envs/prod/aws/1/eu-west-2",
		Warning:    "it wasn't imported there: don't apply the removed block",
	}
	bucket := IncompleteHandover{
		Move:       Move{From: "aws_s3_bucket.logs", FromRoot: "aws/1/eu-west-2", ToRoot: "envs/prod/aws/1/eu-west-2"},
		FailedRoot: "aws/1/eu-west-2",
		Warning:    "both roots have it: apply the removed block first",
	}
	global := IncompleteHandover{
		Move:       Move{From: "aws_iam_role.app", FromRoot: "aws/1/aws-global", ToRoot: "envs/prod/aws/1/aws-global"},
		FailedRoot: "aws/1/aws-global",
		Warning:    "both roots have it",
	}
	r.AddIncompleteHandover(queue)
	r.AddIncompleteHandover(bucket)
	r.AddIncompleteHandover(global)
	r.Finish(nil, nil, false)

	if want := []IncompleteHandover{global, bucket, queue}; !reflect.DeepEqual(r.IncompleteHandovers, want) {
		t.Errorf("handovers %+v, want %+v", r.IncompleteHandovers, want)
	}
	if r.ExitCode != ExitOK {
		t.Errorf("exit code %d, want %d", r.ExitCode, ExitOK)
	}
	doc, md := rendered(t, r)
	handovers, _ := doc["incomplete_handovers"].([]any)
	if len(handovers) != 3 {
		t.Fatalf("JSON handovers %v", doc["incomplete_handovers"])
	}
	if h, _ := handovers[2].(map[string]any); h["failed_root"] != queue.FailedRoot || h["warning"] != queue.Warning || h["from"] != queue.From {
		t.Errorf("JSON handover %v, want %+v", h, queue)
	}
	for _, line := range []string{
		"\n## Handovers not finished\n",
		"- `aws_iam_role.app` from `aws/1/aws-global` to `envs/prod/aws/1/aws-global`: both roots have it\n" +
			"- `aws_s3_bucket.logs` from `aws/1/eu-west-2` to `envs/prod/aws/1/eu-west-2`: both roots have it: apply the removed block first\n" +
			"- `aws_sqs_queue.orders` from `aws/1/eu-west-2` to `envs/prod/aws/1/eu-west-2`: it wasn't imported there: don't apply the removed block\n",
	} {
		if !strings.Contains(md, line) {
			t.Errorf("report.md misses %q:\n%s", line, md)
		}
	}
}

// The provider's version is what the lock file records for its source,
// matched without regard to case, and none when the lock doesn't record it
// or doesn't parse, even where a version was recorded before: the lock is
// the source of truth. The report shows it after the constraint.
func TestLockProvider(t *testing.T) {
	t.Parallel()
	const source = "registry.terraform.io/hashicorp/aws"
	lock := func(source, version string) string {
		return "provider \"" + source + "\" {\n  version     = \"" + version + "\"\n  constraints = \"~> 6.67\"\n}\n"
	}
	for _, tc := range []struct {
		name string
		lock string
		want string
	}{
		{"recorded", lock(source, "6.67.0"), "6.67.0"},
		{"among others", lock("registry.terraform.io/hashicorp/random", "3.7.2") + lock(source, "6.68.1"), "6.68.1"},
		{"another case", lock("registry.terraform.io/HashiCorp/AWS", "6.67.0"), "6.67.0"},
		{"another provider", lock("registry.terraform.io/hashicorp/random", "3.7.2"), ""},
		{"no version", "provider \"" + source + "\" {\n  constraints = \"~> 6.67\"\n}\n", ""},
		{"empty", "", ""},
		{"not HCL", "provider {", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &Report{}
			r.Provider = Provider{Source: source, Constraint: "~> 6.67", Version: "stale"}
			r.LockProvider([]byte(tc.lock))
			if r.Provider.Version != tc.want {
				t.Errorf("version %q, want %q", r.Provider.Version, tc.want)
			}
			doc, md := rendered(t, r)
			provider, _ := doc["provider"].(map[string]any)
			heading := "provider `" + source + "` ~> 6.67."
			if tc.want != "" {
				heading = "provider `" + source + "` ~> 6.67 (" + tc.want + ")."
			}
			if v, _ := provider["version"].(string); v != tc.want || !strings.Contains(md, heading) {
				t.Errorf("JSON provider %v, report.md:\n%s\nwant version %q and %q", provider, md, tc.want, heading)
			}
		})
	}
}

// The exit code of an import that ended with an error is the one set
// after Finish, in the JSON report and coverage.json alike.
func TestSetExitCode(t *testing.T) {
	t.Parallel()
	r := &Report{}
	r.Finish(nil, nil, false)
	if r.ExitCode != ExitOK {
		t.Fatalf("exit code %d before SetExitCode", r.ExitCode)
	}
	r.SetExitCode(2)
	if doc, _ := rendered(t, r); doc["exit_code"] != 2.0 {
		t.Errorf("JSON exit code %v, want 2", doc["exit_code"])
	}
	dir := t.TempDir()
	if err := r.WriteFiles(dir); err != nil {
		t.Fatal(err)
	}
	read, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if read.ExitCode != 2 {
		t.Errorf("coverage.json exit code %d, want 2", read.ExitCode)
	}
}
