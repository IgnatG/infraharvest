// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package selection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/managed"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "selection.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const edited = `version: 1
defaults:
  include: true
rules:
  - exclude: { type: aws_cloudwatch_log_group, id: "/aws/lambda/*" }
  - include: { type: [aws_cloudwatch_log_group], name: "*keep*" }
resources:
  - type: aws_vpc
    id: vpc-default
    include: false
    reason: default VPC, which AWS creates in every region
  - type: aws_vpc
    id: vpc-0abc1234
    include: true
  - type: aws_s3_bucket
    id: old-archive
    include: false
    note: legacy bucket, to be deleted
`

func TestDecide(t *testing.T) {
	f, err := Load(write(t, edited))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		resourceType, id, name string
		want                   Decision
	}{
		{"aws_vpc", "vpc-0abc1234", "", Decision{Include: true}},
		{"aws_vpc", "vpc-default", "", Decision{Reason: "default VPC, which AWS creates in every region"}},
		{"aws_s3_bucket", "old-archive", "", Decision{Reason: "excluded in the selection file"}},
		// Not listed: rules, then defaults.
		{"aws_cloudwatch_log_group", "/aws/lambda/fn", "fn", Decision{Reason: "excluded by a rule in the selection file"}},
		{"aws_cloudwatch_log_group", "/aws/lambda/keep-me", "keep-me", Decision{Include: true}},
		{"aws_sqs_queue", "https://sqs/1/new", "new", Decision{Include: true}},
	} {
		if got := f.Decide(tc.resourceType, tc.id, tc.name); got != tc.want {
			t.Errorf("%s %s: got %+v, want %+v", tc.resourceType, tc.id, got, tc.want)
		}
	}
}

func TestDefaultsExclude(t *testing.T) {
	f, err := Load(write(t, "version: 1\ndefaults:\n  include: false\nresources: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Decide("aws_vpc", "vpc-new", ""); got.Include || got.Reason == "" {
		t.Errorf("got %+v, want excluded with a reason", got)
	}
}

func TestLoadRejectsMistakes(t *testing.T) {
	for name, content := range map[string]string{
		"unknown field":     "version: 1\ndefaults:\n  include: true\nresources:\n  - type: aws_vpc\n    id: a\n    includ: true\n",
		"other version":     "version: 2\nresources: []\n",
		"rule without verb": "version: 1\nrules:\n  - {}\nresources: []\n",
		"tag match":         "version: 1\nrules:\n  - exclude: { tag: { ManagedBy: cloudformation } }\nresources: []\n",
	} {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestSaveRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.yaml")
	f := &File{Version: Version, Defaults: Defaults{Include: true}, Resources: []Resource{
		{Type: "aws_vpc", ID: "vpc-b", Include: true},
		{Type: "aws_vpc", ID: "vpc-a", Name: "default", Include: false, Reason: "default VPC"},
		{Type: "aws_iam_role", ID: "app", Include: true},
	}}

	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), "# infraharvest selection file") {
		t.Errorf("no header:\n%s", content)
	}
	if strings.Index(string(content), "aws_iam_role") > strings.Index(string(content), "vpc-a") || strings.Index(string(content), "vpc-a") > strings.Index(string(content), "vpc-b") {
		t.Errorf("resources not sorted:\n%s", content)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Decide("aws_vpc", "vpc-a", ""); got.Include || got.Reason != "default VPC" {
		t.Errorf("round trip lost the decision: %+v", got)
	}
}

func TestMerge(t *testing.T) {
	f := &File{
		Version:  Version,
		Defaults: Defaults{Include: true},
		Rules:    []Rule{{Exclude: &Match{Type: Patterns{"aws_cloudwatch_log_group"}, ID: Patterns{"/aws/lambda/*"}}}},
		Resources: []Resource{
			{Type: "aws_vpc", ID: "vpc-1", Include: false, Note: "managed by the network team"},
			{Type: "aws_s3_bucket", ID: "gone", Include: true},
		},
	}
	added, dropped := f.Merge([]Resource{
		{Type: "aws_vpc", ID: "vpc-1", Include: true},
		{Type: "aws_s3_bucket", ID: "new-bucket", Include: true},
		{Type: "aws_cloudwatch_log_group", ID: "/aws/lambda/f", Include: true},
		{Type: "aws_vpc", ID: "vpc-default", Reason: "default VPC"},
	})

	if added != 3 || dropped != 1 {
		t.Errorf("added %d, dropped %d", added, dropped)
	}
	want := map[string]Resource{
		"aws_vpc vpc-1":                          {Type: "aws_vpc", ID: "vpc-1", Include: false, Note: "managed by the network team"},
		"aws_s3_bucket new-bucket":               {Type: "aws_s3_bucket", ID: "new-bucket", Include: true, New: true},
		"aws_cloudwatch_log_group /aws/lambda/f": {Type: "aws_cloudwatch_log_group", ID: "/aws/lambda/f", Reason: "excluded by a rule in the selection file", New: true},
		"aws_vpc vpc-default":                    {Type: "aws_vpc", ID: "vpc-default", Reason: "default VPC", New: true},
	}
	if len(f.Resources) != len(want) {
		t.Fatalf("resources: %+v", f.Resources)
	}
	for _, r := range f.Resources {
		if r != want[r.Type+" "+r.ID] {
			t.Errorf("%s %s: got %+v, want %+v", r.Type, r.ID, r, want[r.Type+" "+r.ID])
		}
	}
	if !f.Has("aws_s3_bucket", "new-bucket") || f.Has("aws_s3_bucket", "gone") {
		t.Error("the index doesn't follow the merge")
	}
}

// The same type and ID in two scopes, such as one IAM role name in two
// accounts, are two entries.
func TestScopes(t *testing.T) {
	const a, b = "aws/111122223333/global", "aws/444455556666/global"
	f := &File{Version: Version, Defaults: Defaults{Include: true}, Resources: []Resource{
		{Type: "aws_iam_role", ID: "admin", Scope: a, Include: true},
		{Type: "aws_iam_role", ID: "admin", Scope: b, Include: false, Note: "the other account's"},
	}}

	if !f.HasIn(a, "aws_iam_role", "admin") || !f.HasIn(b, "aws_iam_role", "admin") || !f.Has("aws_iam_role", "admin") {
		t.Error("both entries must be found")
	}
	if f.HasIn("aws/777788889999/global", "aws_iam_role", "admin") {
		t.Error("an entry of another scope must not count")
	}
	if d := f.DecideIn(a, "aws_iam_role", "admin", ""); !d.Include {
		t.Errorf("scope %s: %+v", a, d)
	}
	if d := f.DecideIn(b, "aws_iam_role", "admin", ""); d.Include || d.Reason != "excluded in the selection file" {
		t.Errorf("scope %s: %+v", b, d)
	}

	// Discover lists both again: both are kept, with their decisions.
	added, dropped := f.Merge([]Resource{
		{Type: "aws_iam_role", ID: "admin", Scope: b, Include: true},
		{Type: "aws_iam_role", ID: "admin", Scope: a, Include: true},
	})
	if added != 0 || dropped != 0 || len(f.Resources) != 2 {
		t.Errorf("merge: added %d, dropped %d, resources %+v", added, dropped, f.Resources)
	}
	if d := f.DecideIn(b, "aws_iam_role", "admin", ""); d.Include {
		t.Errorf("the decision for scope %s was lost: %+v", b, d)
	}

	// The file round trips with both.
	path := filepath.Join(t.TempDir(), "selection.yaml")
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Resources) != 2 || loaded.DecideIn(b, "aws_iam_role", "admin", "").Include {
		t.Errorf("round trip: %+v", loaded.Resources)
	}
}

// An entry written without a scope decides the resource in any scope.
func TestScopelessEntryApplies(t *testing.T) {
	f := &File{Version: Version, Defaults: Defaults{Include: true}, Resources: []Resource{
		{Type: "aws_vpc", ID: "vpc-1", Include: false, Note: "the network team's"},
	}}
	const scope = "aws/111122223333/eu-west-2"

	if !f.HasIn(scope, "aws_vpc", "vpc-1") || f.DecideIn(scope, "aws_vpc", "vpc-1", "").Include {
		t.Error("the scope-less entry must decide")
	}
	added, dropped := f.Merge([]Resource{{Type: "aws_vpc", ID: "vpc-1", Scope: scope, Include: true}})
	if added != 0 || dropped != 0 || len(f.Resources) != 1 || f.Resources[0].Include || f.Resources[0].Scope != scope {
		t.Errorf("merge: added %d, dropped %d, resources %+v", added, dropped, f.Resources)
	}
}

// Discover with --managed-state excludes what Terraform manages since the
// file was written, unless a person noted why it is included.
func TestMergeExcludesManaged(t *testing.T) {
	f := &File{Version: Version, Defaults: Defaults{Include: true}, Resources: []Resource{
		{Type: "aws_s3_bucket", ID: "logs", Include: true},
		{Type: "aws_s3_bucket", ID: "assets", Include: true, Note: "moving it to this root"},
		{Type: "aws_s3_bucket", ID: "old", Include: false, Reason: "excluded in the selection file"},
	}}
	reason := managed.Reason + " (prod.tfstate)"

	f.Merge([]Resource{
		{Type: "aws_s3_bucket", ID: "logs", Reason: reason},
		{Type: "aws_s3_bucket", ID: "assets", Reason: reason},
		{Type: "aws_s3_bucket", ID: "old", Reason: reason},
	})

	want := map[string]Resource{
		"logs":   {Type: "aws_s3_bucket", ID: "logs", Include: false, Reason: reason, New: true},
		"assets": {Type: "aws_s3_bucket", ID: "assets", Include: true, Note: "moving it to this root"},
		"old":    {Type: "aws_s3_bucket", ID: "old", Include: false, Reason: "excluded in the selection file"},
	}
	for _, r := range f.Resources {
		if r != want[r.ID] {
			t.Errorf("%s: got %+v, want %+v", r.ID, r, want[r.ID])
		}
	}
}

// A rule decides a new resource before the provider's defaults do.
func TestMergeRulesBeforeDefaults(t *testing.T) {
	f := &File{
		Version:  Version,
		Defaults: Defaults{Include: true},
		Rules:    []Rule{{Include: &Match{Type: Patterns{"aws_vpc"}}}},
	}

	f.Merge([]Resource{{Type: "aws_vpc", ID: "vpc-default", Reason: "default VPC"}})

	if r := f.Resources[0]; !r.Include || r.Reason != "" || !r.New {
		t.Errorf("got %+v, want included by the rule", r)
	}
	if d, ok := f.ByRule("aws_subnet", "subnet-1", ""); ok || d.Include {
		t.Errorf("no rule matches a subnet: %+v, %v", d, ok)
	}
}
