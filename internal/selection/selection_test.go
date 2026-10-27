// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package selection

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/managed"
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
	t.Parallel()
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
	t.Parallel()
	f, err := Load(write(t, "version: 1\ndefaults:\n  include: false\nresources: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Decide("aws_vpc", "vpc-new", ""); got.Include || got.Reason == "" {
		t.Errorf("got %+v, want excluded with a reason", got)
	}
}

func TestLoadRejectsMistakes(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	}, nil)

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
		if !reflect.DeepEqual(r, want[r.Type+" "+r.ID]) {
			t.Errorf("%s %s: got %+v, want %+v", r.Type, r.ID, r, want[r.Type+" "+r.ID])
		}
	}
	if !f.Has("aws_s3_bucket", "new-bucket") || f.Has("aws_s3_bucket", "gone") {
		t.Error("the index doesn't follow the merge")
	}
}

// After a listing that failed in part, the entries it didn't find keep
// their decisions: they may be only what failed to list.
func TestMergeKeepsUnlisted(t *testing.T) {
	t.Parallel()
	f := &File{Version: Version, Defaults: Defaults{Include: true}, Resources: []Resource{
		{Type: "aws_iam_role", ID: "admin", Scope: "aws/222233334444/global", Include: false, Note: "break-glass role"},
		{Type: "aws_vpc", ID: "vpc-1", Scope: "aws/111122223333/eu-west-2", Include: true},
	}}
	added, dropped := f.Merge([]Resource{{Type: "aws_vpc", ID: "vpc-1", Scope: "aws/111122223333/eu-west-2", Include: true}}, func(Resource) bool { return false })
	if added != 0 || dropped != 0 || len(f.Resources) != 2 {
		t.Fatalf("added %d, dropped %d, resources %+v", added, dropped, f.Resources)
	}
	if d := f.decideIn("aws/222233334444/global", "aws_iam_role", "admin", "", nil); d.Include {
		t.Errorf("the unlisted role's exclusion was lost: %+v", d)
	}
}

// A discover of one region drops only that region's entries it no longer
// finds: the other region's keep their decisions and notes.
func TestMergeKeepsWhatItDidNotCover(t *testing.T) {
	t.Parallel()
	const a, b = "aws/111122223333/eu-west-1", "aws/111122223333/eu-west-2"
	f := &File{Version: Version, Defaults: Defaults{Include: true}, Resources: []Resource{
		{Type: "aws_vpc", ID: "vpc-a", Scope: a, Service: "vpc", Include: false, Note: "the network team's"},
		{Type: "aws_vpc", ID: "vpc-b", Scope: b, Service: "vpc", Include: true},
		{Type: "aws_vpc", ID: "vpc-gone", Scope: b, Service: "vpc", Include: true},
	}}
	covered := func(r Resource) bool { return r.Scope == b && r.Service == "vpc" }
	added, dropped := f.Merge([]Resource{{Type: "aws_vpc", ID: "vpc-b", Scope: b, Service: "vpc", Include: true}}, covered)
	if added != 0 || dropped != 1 || len(f.Resources) != 2 {
		t.Fatalf("added %d, dropped %d, resources %+v", added, dropped, f.Resources)
	}
	if !f.hasIn(a, "aws_vpc", "vpc-a") || f.hasIn(b, "aws_vpc", "vpc-gone") {
		t.Errorf("resources %+v", f.Resources)
	}
	if d := f.decideIn(a, "aws_vpc", "vpc-a", "", nil); d.Include {
		t.Errorf("region %s lost its decision: %+v", a, d)
	}
}

// The same type and ID in two scopes, such as one IAM role name in two
// accounts, are two entries.
func TestScopes(t *testing.T) {
	t.Parallel()
	const a, b = "aws/111122223333/global", "aws/444455556666/global"
	f := &File{Version: Version, Defaults: Defaults{Include: true}, Resources: []Resource{
		{Type: "aws_iam_role", ID: "admin", Scope: a, Include: true},
		{Type: "aws_iam_role", ID: "admin", Scope: b, Include: false, Note: "the other account's"},
	}}

	if !f.hasIn(a, "aws_iam_role", "admin") || !f.hasIn(b, "aws_iam_role", "admin") || !f.Has("aws_iam_role", "admin") {
		t.Error("both entries must be found")
	}
	if f.hasIn("aws/777788889999/global", "aws_iam_role", "admin") {
		t.Error("an entry of another scope must not count")
	}
	if d := f.decideIn(a, "aws_iam_role", "admin", "", nil); !d.Include {
		t.Errorf("scope %s: %+v", a, d)
	}
	if d := f.decideIn(b, "aws_iam_role", "admin", "", nil); d.Include || d.Reason != "excluded in the selection file" {
		t.Errorf("scope %s: %+v", b, d)
	}

	// Discover lists both again: both are kept, with their decisions.
	added, dropped := f.Merge([]Resource{
		{Type: "aws_iam_role", ID: "admin", Scope: b, Include: true},
		{Type: "aws_iam_role", ID: "admin", Scope: a, Include: true},
	}, nil)
	if added != 0 || dropped != 0 || len(f.Resources) != 2 {
		t.Errorf("merge: added %d, dropped %d, resources %+v", added, dropped, f.Resources)
	}
	if d := f.decideIn(b, "aws_iam_role", "admin", "", nil); d.Include {
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
	if len(loaded.Resources) != 2 || loaded.decideIn(b, "aws_iam_role", "admin", "", nil).Include {
		t.Errorf("round trip: %+v", loaded.Resources)
	}
}

// An entry written without a scope decides the resource in any scope.
func TestScopelessEntryApplies(t *testing.T) {
	t.Parallel()
	f := &File{Version: Version, Defaults: Defaults{Include: true}, Resources: []Resource{
		{Type: "aws_vpc", ID: "vpc-1", Include: false, Note: "the network team's"},
	}}
	const scope = "aws/111122223333/eu-west-2"

	if !f.hasIn(scope, "aws_vpc", "vpc-1") || f.decideIn(scope, "aws_vpc", "vpc-1", "", nil).Include {
		t.Error("the scope-less entry must decide")
	}
	added, dropped := f.Merge([]Resource{{Type: "aws_vpc", ID: "vpc-1", Scope: scope, Include: true}}, nil)
	if added != 0 || dropped != 0 || len(f.Resources) != 1 || f.Resources[0].Include || f.Resources[0].Scope != scope {
		t.Errorf("merge: added %d, dropped %d, resources %+v", added, dropped, f.Resources)
	}
}

// Discover with --managed-state excludes what Terraform manages since the
// file was written, unless a person noted why it is included.
func TestMergeExcludesManaged(t *testing.T) {
	t.Parallel()
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
	}, nil)

	want := map[string]Resource{
		"logs":   {Type: "aws_s3_bucket", ID: "logs", Include: false, Reason: reason, New: true},
		"assets": {Type: "aws_s3_bucket", ID: "assets", Include: true, Note: "moving it to this root"},
		"old":    {Type: "aws_s3_bucket", ID: "old", Include: false, Reason: "excluded in the selection file"},
	}
	for _, r := range f.Resources {
		if !reflect.DeepEqual(r, want[r.ID]) {
			t.Errorf("%s: got %+v, want %+v", r.ID, r, want[r.ID])
		}
	}
}

// A rule decides a new resource before the provider's defaults do.
func TestMergeRulesBeforeDefaults(t *testing.T) {
	t.Parallel()
	f := &File{
		Version:  Version,
		Defaults: Defaults{Include: true},
		Rules:    []Rule{{Include: &Match{Type: Patterns{"aws_vpc"}}}},
	}

	f.Merge([]Resource{{Type: "aws_vpc", ID: "vpc-default", Reason: "default VPC"}}, nil)

	if r := f.Resources[0]; !r.Include || r.Reason != "" || !r.New {
		t.Errorf("got %+v, want included by the rule", r)
	}
}

const byTag = `version: 1
defaults:
  include: true
rules:
  - exclude: { tags: { env: dev } }
  - exclude: { type: aws_s3_bucket, tags: { owner: "" } }
  - include: { tags: { env: dev, keep: ["yes", "true"] } }
resources: []
`

func TestRulesMatchTags(t *testing.T) {
	t.Parallel()
	f, err := Load(write(t, byTag))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		resourceType string
		tags         map[string]string
		include      bool
	}{
		{"aws_vpc", map[string]string{"env": "dev"}, false},
		{"aws_vpc", map[string]string{"env": "prod"}, true},
		{"aws_vpc", nil, true},
		// An empty pattern matches a resource without the tag.
		{"aws_s3_bucket", map[string]string{"env": "prod"}, false},
		{"aws_s3_bucket", map[string]string{"env": "prod", "owner": "data"}, true},
		// Every tag a match names must match.
		{"aws_vpc", map[string]string{"env": "dev", "keep": "yes"}, true},
		{"aws_vpc", map[string]string{"env": "dev", "keep": "no"}, false},
	} {
		if got := f.decideIn("", tc.resourceType, "id", "", tc.tags); got.Include != tc.include {
			t.Errorf("%s %v: got %+v, want include %v", tc.resourceType, tc.tags, got, tc.include)
		}
	}
}

// Running discover again updates the tags of the entries it keeps, and a
// rule by tag decides the new ones.
func TestMergeUpdatesTags(t *testing.T) {
	t.Parallel()
	f, err := Load(write(t, byTag))
	if err != nil {
		t.Fatal(err)
	}
	f.Merge([]Resource{{Type: "aws_vpc", ID: "vpc-1", Include: true, Tags: map[string]string{"env": "prod"}}}, nil)
	f.Resources[0].Note = "reviewed"
	f.Merge([]Resource{
		{Type: "aws_vpc", ID: "vpc-1", Include: true, Tags: map[string]string{"env": "prod", "team": "net"}},
		{Type: "aws_vpc", ID: "vpc-2", Include: true, Tags: map[string]string{"env": "dev"}},
	}, nil)
	for _, r := range f.Resources {
		switch r.ID {
		case "vpc-1":
			if r.Tags["team"] != "net" || r.Note != "reviewed" || !r.Include {
				t.Errorf("kept entry: %+v", r)
			}
		case "vpc-2":
			if r.Include || !r.New {
				t.Errorf("new dev entry: %+v, want excluded by the rule", r)
			}
		}
	}

	path := filepath.Join(t.TempDir(), "selection.yaml")
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.hasIn("", "aws_vpc", "vpc-1") || saved.Resources[0].Tags["env"] != "prod" {
		t.Errorf("saved tags: %+v", saved.Resources)
	}
}

// A pattern is compiled once, and one without * matches only itself.
func TestPatternsMatch(t *testing.T) {
	t.Parallel()
	first, again := glob("/aws/lambda/*"), glob("/aws/lambda/*")
	if first != again {
		t.Error("the pattern was compiled twice")
	}
	for _, tc := range []struct {
		patterns Patterns
		value    string
		want     bool
	}{
		{nil, "anything", true},
		{Patterns{"aws_vpc"}, "aws_vpc", true},
		{Patterns{"aws_vpc"}, "aws_vpc_endpoint", false},
		{Patterns{"a.c"}, "abc", false},
		{Patterns{"aws_*"}, "aws_vpc", true},
		{Patterns{"*_vpc", "aws_s3_*"}, "aws_s3_bucket", true},
		{Patterns{"/aws/lambda/*"}, "/aws/ecs/x", false},
	} {
		if got := tc.patterns.match(tc.value); got != tc.want {
			t.Errorf("%q match %q = %v, want %v", tc.patterns, tc.value, got, tc.want)
		}
	}
}

// A rule doesn't include a new resource that another Terraform state or
// tool manages: it stays excluded with its reason, unless its entry or
// IncludeOwned includes it. What the cloud manages, such as a default
// VPC, a rule includes; an exclude rule excludes all alike.
func TestMergeRulesKeepOwnedOut(t *testing.T) {
	t.Parallel()
	prod := map[string]string{"Environment": "prod"}
	stack := "managed by CloudFormation stack app"
	pulumi := "managed by Pulumi (per its tag ManagedBy=pulumi; --include-owned imports it)"
	terraform := managed.Reason + " (Terraform elsewhere, per its tag ManagedBy=terraform; --include-owned imports it)"
	listed := []Resource{
		{Type: "aws_s3_bucket", ID: "stack-bucket", Tags: prod, Reason: stack},
		{Type: "aws_sqs_queue", ID: "pulumi-queue", Tags: prod, Reason: pulumi},
		{Type: "aws_sns_topic", ID: "terraform-topic", Tags: prod, Reason: terraform},
		{Type: "aws_vpc", ID: "vpc-default", Tags: prod, Reason: "default VPC"},
		{Type: "aws_s3_bucket", ID: "app-bucket", Tags: prod, Include: true},
		{Type: "aws_s3_bucket", ID: "kept", Tags: prod, Reason: stack},
	}
	include := []Rule{{Include: &Match{Tags: map[string]Patterns{"Environment": {"prod"}}}}}
	newFile := func(rules []Rule, includeOwned bool) *File {
		return &File{
			Version:      Version,
			Rules:        rules,
			IncludeOwned: includeOwned,
			Resources:    []Resource{{Type: "aws_s3_bucket", ID: "kept", Include: true, Note: "moving it here"}},
		}
	}
	decisions := func(f *File) map[string]Resource {
		f.Merge(listed, nil)
		got := map[string]Resource{}
		for _, r := range f.Resources {
			got[r.ID] = r
		}
		return got
	}
	for _, tc := range []struct {
		name  string
		file  *File
		want  map[string]bool
		owned bool // whether excluded owned resources keep their reason
	}{
		{"include rule", newFile(include, false), map[string]bool{"stack-bucket": false, "pulumi-queue": false, "terraform-topic": false, "vpc-default": true, "app-bucket": true, "kept": true}, true},
		{"include rule with IncludeOwned", newFile(include, true), map[string]bool{"stack-bucket": true, "pulumi-queue": true, "terraform-topic": true, "vpc-default": true, "app-bucket": true, "kept": true}, false},
		{"exclude rule", newFile([]Rule{{Exclude: &Match{Tags: map[string]Patterns{"Environment": {"prod"}}}}}, false), map[string]bool{"stack-bucket": false, "pulumi-queue": false, "terraform-topic": false, "vpc-default": false, "app-bucket": false, "kept": true}, false},
	} {
		got := decisions(tc.file)
		for id, include := range tc.want {
			r := got[id]
			if r.Include != include {
				t.Errorf("%s: %s: got %+v, want include %v", tc.name, id, r, include)
			}
		}
		if tc.owned {
			for id, reason := range map[string]string{"stack-bucket": stack, "pulumi-queue": pulumi, "terraform-topic": terraform} {
				if got[id].Reason != reason {
					t.Errorf("%s: %s: reason %q, want %q", tc.name, id, got[id].Reason, reason)
				}
			}
		}
		if r := got["app-bucket"]; r.Include == (r.Reason != "") {
			t.Errorf("%s: app-bucket: %+v", tc.name, r)
		}
	}
}

// Only what another Terraform state or tool manages is owned.
func TestOwned(t *testing.T) {
	t.Parallel()
	for reason, want := range map[string]bool{
		managed.Reason + " (prod.tfstate)":                                 true,
		"managed by CloudFormation stack app":                              true,
		"default VPC, which AWS creates in every region":                   false,
		"shared by account 111122223333 through AWS RAM, which manages it": false,
		"": false,
	} {
		if got := owned(reason); got != want {
			t.Errorf("%q: got %v, want %v", reason, got, want)
		}
	}
}

// Running discover again brings the decisions it made up to date with what
// manages each resource now, and leaves people's decisions as they are.
func TestMergeRefreshesReasons(t *testing.T) {
	t.Parallel()
	prod := managed.Reason + " (prod.tfstate)"
	dev := managed.Reason + " (dev.tfstate)"
	tag := managed.Reason + " (Terraform elsewhere, per its tag ManagedBy=terraform; --include-owned imports it)"
	stack := "managed by CloudFormation stack app"
	pulumi := "managed by Pulumi (per its tag ManagedBy=pulumi; --include-owned imports it)"
	f := &File{
		Version:  Version,
		Defaults: Defaults{Include: true},
		Rules:    []Rule{{Exclude: &Match{Type: Patterns{"aws_sqs_queue"}}}},
		Refresh:  Refresh{State: true, Tags: true, Defaults: true},
		Resources: []Resource{
			{Type: "aws_s3_bucket", ID: "to-stack", Include: true},
			{Type: "aws_s3_bucket", ID: "to-pulumi", Include: true},
			{Type: "aws_vpc", ID: "vpc-default", Include: true, Reason: "default VPC"},
			{Type: "aws_s3_bucket", ID: "noted", Include: true, Note: "ours now"},
			{Type: "aws_s3_bucket", ID: "left-state", Reason: prod},
			{Type: "aws_s3_bucket", ID: "left-tag", Reason: tag},
			{Type: "aws_s3_bucket", ID: "state-to-stack", Reason: prod},
			{Type: "aws_s3_bucket", ID: "other-state", Reason: prod},
			{Type: "aws_s3_bucket", ID: "excluded", Include: false},
			{Type: "aws_s3_bucket", ID: "by-rule", Reason: "excluded by a rule in the selection file"},
			{Type: "aws_sqs_queue", ID: "jobs", Reason: prod},
		},
	}

	f.Merge([]Resource{
		{Type: "aws_s3_bucket", ID: "to-stack", Reason: stack},
		{Type: "aws_s3_bucket", ID: "to-pulumi", Reason: pulumi},
		{Type: "aws_vpc", ID: "vpc-default", Reason: stack},
		{Type: "aws_s3_bucket", ID: "noted", Reason: stack},
		{Type: "aws_s3_bucket", ID: "left-state", Include: true},
		{Type: "aws_s3_bucket", ID: "left-tag", Include: true},
		{Type: "aws_s3_bucket", ID: "state-to-stack", Reason: stack},
		{Type: "aws_s3_bucket", ID: "other-state", Reason: dev},
		{Type: "aws_s3_bucket", ID: "excluded", Include: true},
		{Type: "aws_s3_bucket", ID: "by-rule", Include: true},
		{Type: "aws_sqs_queue", ID: "jobs", Include: true},
	}, nil)

	want := map[string]Resource{
		"to-stack":       {Type: "aws_s3_bucket", ID: "to-stack", Reason: stack, New: true},
		"to-pulumi":      {Type: "aws_s3_bucket", ID: "to-pulumi", Reason: pulumi, New: true},
		"vpc-default":    {Type: "aws_vpc", ID: "vpc-default", Include: true, Reason: "default VPC"},
		"noted":          {Type: "aws_s3_bucket", ID: "noted", Include: true, Note: "ours now"},
		"left-state":     {Type: "aws_s3_bucket", ID: "left-state", Include: true, New: true},
		"left-tag":       {Type: "aws_s3_bucket", ID: "left-tag", Include: true, New: true},
		"state-to-stack": {Type: "aws_s3_bucket", ID: "state-to-stack", Reason: stack, New: true},
		"other-state":    {Type: "aws_s3_bucket", ID: "other-state", Reason: dev},
		"excluded":       {Type: "aws_s3_bucket", ID: "excluded"},
		"by-rule":        {Type: "aws_s3_bucket", ID: "by-rule", Reason: "excluded by a rule in the selection file"},
		"jobs":           {Type: "aws_sqs_queue", ID: "jobs", Reason: "excluded by a rule in the selection file", New: true},
	}
	if len(f.Resources) != len(want) {
		t.Fatalf("resources: %+v", f.Resources)
	}
	for _, r := range f.Resources {
		if !reflect.DeepEqual(r, want[r.ID]) {
			t.Errorf("%s: got %+v, want %+v", r.ID, r, want[r.ID])
		}
	}
}

// A reason of a kind the run didn't work out again, such as without
// --managed-state, or after tags that failed to read, stays as it is.
func TestMergeKeepsReasonsItDidNotWorkOut(t *testing.T) {
	t.Parallel()
	state := managed.Reason + " (prod.tfstate)"
	tag := "managed by Pulumi (per its tag ManagedBy=pulumi; --include-owned imports it)"
	for _, refresh := range []Refresh{{}, {Defaults: true}} {
		f := &File{Version: Version, Defaults: Defaults{Include: true}, Refresh: refresh, Resources: []Resource{
			{Type: "aws_s3_bucket", ID: "state", Reason: state},
			{Type: "aws_s3_bucket", ID: "tag", Reason: tag},
		}}

		f.Merge([]Resource{
			{Type: "aws_s3_bucket", ID: "state", Reason: "default bucket"},
			{Type: "aws_s3_bucket", ID: "tag", Include: true},
		}, nil)

		for _, r := range f.Resources {
			if r.Include || r.New || (r.ID == "state" && r.Reason != state) || (r.ID == "tag" && r.Reason != tag) {
				t.Errorf("%+v: %s changed: %+v", refresh, r.ID, r)
			}
		}
	}
}

// A first file lists each resource once, nothing in it new, and includes
// what the defaults don't exclude.
func TestNewFile(t *testing.T) {
	t.Parallel()
	f := NewFile([]Resource{
		{Type: "aws_vpc", ID: "vpc-1", Include: true},
		{Type: "aws_vpc", ID: "vpc-1", Include: true},
		{Type: "aws_vpc", ID: "vpc-default", Reason: "default VPC"},
	})
	want := []Resource{{Type: "aws_vpc", ID: "vpc-1", Include: true}, {Type: "aws_vpc", ID: "vpc-default", Reason: "default VPC"}}
	if f.Version != Version || !f.Defaults.Include || !reflect.DeepEqual(f.Resources, want) {
		t.Errorf("got %+v", f)
	}
}

// The summary counts each entry as an import decides it, by type too, and
// names the exclusions in the file's order with their reasons.
func TestSummary(t *testing.T) {
	t.Parallel()
	f := &File{Version: Version, Resources: []Resource{
		{Type: "aws_vpc", ID: "vpc-default", Reason: "default VPC"},
		{Type: "aws_s3_bucket", ID: "logs", Include: true},
		{Type: "aws_vpc", ID: "vpc-1", Include: true},
		{Type: "aws_s3_bucket", ID: "old"},
	}}
	want := Summary{
		Included:   2,
		Excluded:   2,
		Types:      []TypeCount{{Type: "aws_s3_bucket", Included: 1, Excluded: 1}, {Type: "aws_vpc", Included: 1, Excluded: 1}},
		Exclusions: []Exclusion{{Type: "aws_vpc", ID: "vpc-default", Reason: "default VPC"}, {Type: "aws_s3_bucket", ID: "old", Reason: "excluded in the selection file"}},
	}
	if got := f.Summary(); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
