// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/managed"
)

func sample() *Report {
	return &Report{
		Tool:     Component{Name: "infraharvest", Version: "v0.1.0"},
		Engine:   Component{Name: "terraform", Version: "1.16.5"},
		Provider: Provider{Source: "registry.terraform.io/hashicorp/aws", Constraint: "~> 6.67", Version: "6.67.0"},
		Directories: []Directory{
			{Path: "aws/us-east-1", Imported: []Resource{
				{Address: "aws_sqs_queue.jobs", ID: "https://sqs/1/jobs"},
				{Address: "aws_ssm_parameter.token", ID: "/app/token"},
			}, LeftOut: []LeftOut{
				{Address: "aws_lb.web", ID: "arn:lb", Errors: []string{"Invalid combination of arguments"}},
			}, Secrets: []Secret{
				{Variable: "aws_ssm_parameter_token_value", Address: "aws_ssm_parameter.token", Attribute: "value"},
			}},
			{Path: "aws", Error: "terraform init: no network"},
		},
		Skipped: []Skipped{{Type: "aws_main_route_table_association", Count: 1, Reason: "Terraform can't import this resource type"}},
	}
}

func TestFinish(t *testing.T) {
	t.Parallel()
	r := sample()
	discovered := map[string]int{"aws_sqs_queue": 1, "aws_ssm_parameter": 1, "aws_lb": 1, "aws_iam_role": 2, "aws_main_route_table_association": 1}

	r.Finish(discovered, map[string]int{"aws_iam_role": 2}, false)

	if r.Directories[0].Path != "aws" {
		t.Errorf("directories not sorted: %+v", r.Directories)
	}
	want := CoverageTotal{Discovered: 6, Imported: 2, LeftOut: 1, Skipped: 1, Failed: 2}
	if r.Totals != want {
		t.Errorf("totals: got %+v, want %+v", r.Totals, want)
	}
	if r.Types[0].Type != "aws_iam_role" || r.Types[0].Failed != 2 {
		t.Errorf("types not sorted or counted: %+v", r.Types)
	}
	if r.ExitCode != ExitIncomplete || r.SchemaVersion != SchemaVersion {
		t.Errorf("exit code %d, schema %d", r.ExitCode, r.SchemaVersion)
	}

	r.Finish(discovered, map[string]int{"aws_iam_role": 2}, true)
	if r.ExitCode != ExitPartial {
		t.Errorf("with --allow-partial: exit code %d, want %d", r.ExitCode, ExitPartial)
	}
}

// Types infraharvest doesn't import don't make an import incomplete.
func TestFinishComplete(t *testing.T) {
	t.Parallel()
	r := &Report{
		Directories: []Directory{{Path: "aws", Imported: []Resource{{Address: "aws_vpc.main", ID: "vpc-1"}}}},
		Skipped:     []Skipped{{Type: "aws_main_route_table_association", Count: 1}},
	}

	r.Finish(map[string]int{"aws_vpc": 1, "aws_main_route_table_association": 1}, nil, false)

	if r.ExitCode != ExitOK || r.Incomplete() {
		t.Errorf("want a complete import, got exit code %d", r.ExitCode)
	}
}

func TestWriteFilesIsDeterministic(t *testing.T) {
	t.Parallel()
	read := func() map[string]string {
		out := t.TempDir()
		r := sample()
		r.Finish(map[string]int{"aws_sqs_queue": 1}, nil, false)
		if err := r.WriteFiles(out); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{}
		for _, name := range []string{"coverage.json", "manifest.json", "report.md"} {
			content, err := os.ReadFile(filepath.Join(out, Dir, name))
			if err != nil {
				t.Fatal(err)
			}
			files[name] = string(content)
		}
		return files
	}

	first, second := read(), read()
	for name, content := range first {
		if second[name] != content {
			t.Errorf("%s differs between runs", name)
		}
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(first["manifest.json"]), &manifest); err != nil {
		t.Fatal(err)
	}
	if _, ok := manifest["directories"]; ok {
		t.Error("manifest.json has coverage fields")
	}
	if !strings.Contains(first["coverage.json"], `"exit_code": 1`) {
		t.Errorf("coverage.json has no exit code:\n%s", first["coverage.json"])
	}
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()
	r := sample()
	r.Finish(map[string]int{"aws_sqs_queue": 1}, nil, true)
	var out bytes.Buffer

	if err := r.WriteJSON(&out); err != nil {
		t.Fatal(err)
	}

	var got struct {
		SchemaVersion int `json:"schema_version"`
		Engine        struct {
			Name string `json:"name"`
		} `json:"engine"`
		Directories []struct {
			LeftOut []struct {
				Address string `json:"address"`
			} `json:"left_out"`
		} `json:"directories"`
		ExitCode int `json:"exit_code"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, out.String())
	}
	if got.SchemaVersion != SchemaVersion || got.Engine.Name != "terraform" || got.ExitCode != ExitPartial ||
		len(got.Directories) != 2 || got.Directories[1].LeftOut[0].Address != "aws_lb.web" {
		t.Errorf("unexpected report:\n%s", out.String())
	}
}

func TestMarkdown(t *testing.T) {
	t.Parallel()
	r := sample()
	r.Unused = []UnusedModule{{Source: "acme/bucket/aws", Version: "1.0.0", Accepts: ">= 5.0, < 6.0"}}
	r.Finish(map[string]int{"aws_sqs_queue": 1, "aws_ssm_parameter": 1, "aws_lb": 1}, nil, false)

	got := r.Markdown()

	for _, want := range []string{
		"infraharvest v0.1.0, terraform 1.16.5, provider `registry.terraform.io/hashicorp/aws` ~> 6.67 (6.67.0).",
		"| 3 | 2 | 0 | 1 | 1 | 0 |",
		"| `aws_lb` | 1 | 0 | 0 | 1 | 0 | 0 |",
		// Nothing is managed, but what isn't is counted all the same.
		"## Managed and unmanaged",
		"| `aws_lb` | 1 | 0 | 0 | 0 | 1 |",
		"- `aws_lb.web` in `aws/us-east-1`: Invalid combination of arguments",
		"- `aws_ssm_parameter_token_value` in `aws/us-east-1`: value of `aws_ssm_parameter.token`",
		"- `aws_main_route_table_association` (1): Terraform can't import this resource type",
		"`acme/bucket/aws` 1.0.0 isn't called: it accepts the provider at `>= 5.0, < 6.0` only, not its newest major release, so its resources stay in the root.",
		"- `aws`: terraform init: no network",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestManagedCounts(t *testing.T) {
	t.Parallel()
	r := &Report{}
	r.Excluded = []Excluded{
		{Type: "aws_vpc", ID: "vpc-1", Reason: "default VPC, which AWS creates in every region"},
		{Type: "aws_s3_bucket", ID: "logs", Reason: managed.Reason + " (s3://acme-state/imported/aws/terraform.tfstate)"},
	}
	r.Finish(map[string]int{"aws_vpc": 1, "aws_s3_bucket": 2}, nil, false)

	if r.Totals.Managed != 1 || r.Totals.Cloud != 1 || r.Totals.Excluded != 2 {
		t.Errorf("totals: %+v", r.Totals)
	}
	if md := r.Markdown(); !strings.Contains(md, "## Managed and unmanaged") || !strings.Contains(md, "| `aws_s3_bucket` | 2 | 1 | 0 | 0 | 1 |") || !strings.Contains(md, "| `aws_vpc` | 1 | 0 | 0 | 1 | 0 |") {
		t.Errorf("markdown:\n%s", md)
	}
}

// What an incremental import leaves out as a root of the output has it
// counts as Terraform's, by the owner the import gave it, whatever its
// reason says; an exclusion without one, as in an older report, counts by
// its reason.
func TestManagedCountsWhatTheOutputsRootsHave(t *testing.T) {
	t.Parallel()
	r := &Report{}
	r.Excluded = []Excluded{
		{Type: "aws_iam_role", ID: "app", Reason: "already in the root", Owner: OwnerTerraformRoot},
		{Type: "aws_iam_role", ID: "ci", Reason: "already in another root of the output, aws/eu-west-2", Owner: OwnerTerraformRoot},
		{Type: "aws_vpc", ID: "vpc-1", Reason: "default VPC, which AWS creates in every region"},
	}
	r.Finish(map[string]int{"aws_iam_role": 2, "aws_vpc": 1}, nil, false)

	if r.Totals.Managed != 2 || r.Totals.Cloud != 1 || r.Totals.Excluded != 3 {
		t.Errorf("totals: %+v", r.Totals)
	}
	var read struct{ Excluded []Excluded }
	if err := json.Unmarshal([]byte(`{"excluded": [{"type": "aws_vpc", "id": "vpc-1", "reason": "default VPC"}]}`), &read); err != nil || len(read.Excluded) != 1 || read.Excluded[0].ManagedBy() != OwnerCloud {
		t.Errorf("an exclusion of an older report: %+v, %v", read.Excluded, err)
	}
}

// A resource in a module call counts as one of its type.
func TestFinishCountsResourcesInModuleCalls(t *testing.T) {
	t.Parallel()
	r := &Report{Directories: []Directory{{Path: "aws", Imported: []Resource{
		{Address: "module.app.aws_iam_role.this", ID: "app"},
		{Address: "aws_iam_role.web", ID: "web"},
	}}}}

	r.Finish(map[string]int{"aws_iam_role": 2}, nil, false)

	if len(r.Types) != 1 || r.Types[0].Type != "aws_iam_role" || r.Types[0].Imported != 2 || r.ExitCode != ExitOK {
		t.Errorf("types %+v, exit code %d, want both roles imported", r.Types, r.ExitCode)
	}
}

// What another tool manages, and every count by account and region.
func TestScopeCounts(t *testing.T) {
	t.Parallel()
	const prod, dev = "aws/111111111111/eu-west-2", "aws/222222222222/eu-west-2"
	r := &Report{}
	r.AddDiscovered(prod, 3)
	r.AddDiscovered(dev, 1)
	r.AddDiscovered(prod, 1)
	r.Excluded = []Excluded{
		{Type: "aws_s3_bucket", ID: "cdk-assets", Reason: OtherToolPrefix + "CloudFormation stack CDKToolkit", Scope: prod},
		{Type: "aws_s3_bucket", ID: "state", Reason: managed.Reason + " (backend)", Scope: prod},
	}
	r.Directories = []Directory{
		{Path: prod, Scope: prod, Imported: []Resource{{Address: "aws_s3_bucket.logs", ID: "logs"}, {Address: "aws_sqs_queue.jobs", ID: "jobs"}}},
		{Path: dev, Scope: dev, Imported: []Resource{{Address: "aws_s3_bucket.dev", ID: "dev"}}},
	}
	r.Finish(map[string]int{"aws_s3_bucket": 4, "aws_sqs_queue": 1}, nil, false)

	want := []ScopeCount{
		{Scope: prod, Discovered: 4, Imported: 2, Excluded: 2, Managed: 1, OtherTool: 1},
		{Scope: dev, Discovered: 1, Imported: 1},
	}
	sort.Slice(want, func(i, j int) bool { return want[i].Scope < want[j].Scope })
	if !reflect.DeepEqual(r.Scopes, want) {
		t.Errorf("scopes: got %+v, want %+v", r.Scopes, want)
	}
	if r.Totals.OtherTool != 1 || r.Totals.Managed != 1 {
		t.Errorf("totals: %+v", r.Totals)
	}
	md := r.Markdown()
	for _, want := range []string{"| `" + prod + "` | 4 | 1 | 1 | 0 | 2 |", "| `" + dev + "` | 1 | 0 | 0 | 0 | 1 |", "| `aws_s3_bucket` | 4 | 1 | 1 | 0 | 2 |"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown misses %q:\n%s", want, md)
		}
	}
}

func TestCountsByTag(t *testing.T) {
	t.Parallel()
	scope := "aws/1/eu-west-2"
	r := &Report{
		TagKeys: []string{"team"},
		Directories: []Directory{{Path: "aws/1/eu-west-2", Scope: scope,
			Imported: []Resource{{Address: "aws_sqs_queue.jobs", ID: "jobs"}},
			LeftOut:  []LeftOut{{Address: "aws_lb.web", ID: "arn:lb"}},
		}},
		Excluded: []Excluded{
			{Type: "aws_s3_bucket", ID: "state", Reason: managed.Reason + " (s3://state)", Scope: scope},
			{Type: "aws_s3_bucket", ID: "stack", Reason: OtherToolPrefix + "CloudFormation stack app", Scope: scope},
			{Type: "aws_vpc", ID: "vpc-1", Reason: "excluded in the selection file", Scope: scope},
		},
	}
	other := &Report{TagKeys: []string{"team"}}
	for _, l := range []Listed{
		{Scope: scope, Type: "aws_sqs_queue", ID: "jobs", Tags: map[string]string{"team": "data"}},
		{Scope: scope, Type: "aws_lb", ID: "arn:lb", Tags: map[string]string{"team": "web"}},
		{Scope: scope, Type: "aws_s3_bucket", ID: "state", Tags: map[string]string{"team": "data"}},
		{Scope: scope, Type: "aws_s3_bucket", ID: "stack"},
	} {
		r.AddListed(l)
	}
	// Listed in another account of the same import.
	other.AddListed(Listed{Scope: scope, Type: "aws_vpc", ID: "vpc-1", Tags: map[string]string{"team": "web", "env": "dev"}})
	merged := &Report{}
	merged.Merge(r)
	merged.Merge(other)
	merged.Finish(nil, nil, false)

	want := []TagCount{
		{Key: "team", Value: "", Discovered: 1, Excluded: 1, OtherTool: 1},
		{Key: "team", Value: "data", Discovered: 2, Imported: 1, Excluded: 1, Managed: 1},
		{Key: "team", Value: "web", Discovered: 2, LeftOut: 1, Excluded: 1},
	}
	if !reflect.DeepEqual(merged.Tags, want) {
		t.Errorf("got %+v\nwant %+v", merged.Tags, want)
	}
	md := merged.Markdown()
	for _, line := range []string{"## By tag", "| `team` | Discovered |", "| (not tagged) | 1 | 0 | 0 | 1 | 0 | 0 |", "| `data` | 2 | 1 | 1 | 0 | 0 | 1 |"} {
		if !strings.Contains(md, line) {
			t.Errorf("report.md lacks %q:\n%s", line, md)
		}
	}

	// Without --report-tags, nothing is counted by tag.
	none := &Report{}
	none.Merge(&Report{listed: r.listed})
	none.Finish(nil, nil, false)
	if none.Tags != nil || strings.Contains(none.Markdown(), "By tag") {
		t.Errorf("counted by tag without keys: %+v", none.Tags)
	}
}

func TestMerge(t *testing.T) {
	t.Parallel()
	var r Report
	first := Report{Tool: Component{Name: "infraharvest", Version: "1.0.0"}, Provider: Provider{Source: "hashicorp/aws"}}
	first.Directories = []Directory{{Path: "aws/111111111111/eu-west-2", Scope: "aws/111111111111/eu-west-2"}}
	first.AddDiscovered("aws/111111111111/eu-west-2", 3)
	second := Report{Tool: Component{Name: "infraharvest", Version: "1.0.0"}, Provider: Provider{Source: "hashicorp/aws", Version: "6.1.0"}}
	second.Directories = []Directory{{Path: "aws/222222222222/eu-west-2", Scope: "aws/222222222222/eu-west-2"}}
	second.Excluded = []Excluded{{Type: "aws_vpc", ID: "vpc-1", Reason: "default VPC", Scope: "aws/222222222222/eu-west-2"}}
	second.Failures = []string{"aws/222222222222/eu-west-2: a check failed"}
	first.Warnings = []string{"aws couldn't say which account it lists in"}
	second.Warnings = first.Warnings
	second.AddDiscovered("aws/222222222222/eu-west-2", 2)
	second.AddDiscovered("aws/111111111111/eu-west-2", 1)

	r.Merge(&first)
	r.Merge(&second)

	if r.Tool.Name != "infraharvest" || r.Provider.Version != "6.1.0" {
		t.Errorf("manifest %+v", r.Manifest)
	}
	if len(r.Directories) != 2 || len(r.Excluded) != 1 || len(r.Failures) != 1 {
		t.Errorf("coverage %+v", r.Coverage)
	}
	if len(r.Scopes) != 2 || r.Scopes[0].Discovered != 4 || r.Scopes[1].Discovered != 2 {
		t.Errorf("scopes %+v", r.Scopes)
	}
	if r.Finish(nil, nil, false); len(r.Warnings) != 1 {
		t.Errorf("warnings %v, want the one both have", r.Warnings)
	}
}

// A check that couldn't run is listed as skipped, not as failed, and
// doesn't make the import incomplete.
func TestSkippedChecks(t *testing.T) {
	t.Parallel()
	r := &Report{Directories: []Directory{{Path: "aws", Checks: []Check{
		{Name: "G1 format", Passed: true},
		{Name: "G5 scanners", Passed: true, Skipped: true, Details: []string{"no scanner installed (tflint, trivy, checkov)"}},
	}}}}

	if got, want := r.SkippedChecks(), []string{"aws: G5 scanners: no scanner installed (tflint, trivy, checkov)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("skipped checks: %v, want %v", got, want)
	}
	if len(r.FailedChecks()) != 0 || r.Incomplete() {
		t.Errorf("a skipped check counts as failed: %v", r.FailedChecks())
	}
	if md := r.Markdown(); !strings.Contains(md, "## Skipped checks") || !strings.Contains(md, "- aws: G5 scanners: no scanner installed") || strings.Contains(md, "## Failed checks") {
		t.Errorf("report:\n%s", md)
	}
}

// Failures are of every part of the import, not only of services that
// couldn't be listed, and the report says so.
func TestMarkdownFailures(t *testing.T) {
	t.Parallel()
	r := &Report{Failures: []string{"account 111122223333: assume role: access denied", "service s3: access denied"}}
	md := r.Markdown()
	if strings.Contains(md, "Failed services") || !strings.Contains(md, "## Failures") {
		t.Errorf("markdown:\n%s", md)
	}
	for _, f := range r.Failures {
		if !strings.Contains(md, f) {
			t.Errorf("markdown misses %q:\n%s", f, md)
		}
	}
}

// The manifests of a run's Import calls add up: the newer and unused
// modules of each, once, and the provider a call held back.
func TestManifestAdd(t *testing.T) {
	t.Parallel()
	var m Manifest
	m.Add(Manifest{Tool: Component{Name: "infraharvest"}, Provider: Provider{Source: "registry.terraform.io/hashicorp/aws", Constraint: "~> 6.67", Version: "6.67.0"},
		Modules: []ModuleVersion{{Source: "terraform-aws-modules/vpc/aws", Version: "6.0.0", Latest: "6.1.0"}}})
	m.Add(Manifest{Tool: Component{Name: "infraharvest"}, Provider: Provider{Source: "registry.terraform.io/hashicorp/aws", Constraint: "~> 6.50", HeldBack: "terraform-aws-modules/eks/aws 21.0.0 accepts < 6.51"},
		Modules: []ModuleVersion{{Source: "terraform-aws-modules/vpc/aws", Version: "6.0.0", Latest: "6.1.0"}, {Source: "terraform-aws-modules/eks/aws", Version: "21.0.0", Latest: "21.1.0"}},
		Unused:  []UnusedModule{{Source: "terraform-aws-modules/old/aws", Version: "1.0.0", Accepts: "< 6.0"}}})
	if len(m.Modules) != 2 || len(m.Unused) != 1 {
		t.Errorf("modules %+v, unused %+v", m.Modules, m.Unused)
	}
	if m.Provider.HeldBack == "" || m.Provider.Constraint != "~> 6.50" || m.Provider.Version != "6.67.0" {
		t.Errorf("provider %+v", m.Provider)
	}
}

// What the roots have that is no longer in the cloud is listed once each,
// sorted, in report.md, coverage.json and the JSON report, from every
// part of the import, and makes the import neither incomplete nor
// counted: it is information about the roots, not about what was listed.
func TestNotInCloud(t *testing.T) {
	t.Parallel()
	orders := NotInCloud{Path: "aws/111111111111/eu-west-2", Address: "aws_sqs_queue.orders", ID: "https://sqs.eu-west-2.amazonaws.com/111111111111/orders", Scope: "aws/111111111111/eu-west-2"}
	jobs := NotInCloud{Path: "aws/111111111111/eu-west-2", Address: "aws_sqs_queue.jobs", ID: "https://sqs.eu-west-2.amazonaws.com/111111111111/jobs", Scope: "aws/111111111111/eu-west-2"}
	other := NotInCloud{Path: "aws/222222222222/eu-west-2", Address: "aws_sqs_queue.orders", ID: "https://sqs.eu-west-2.amazonaws.com/222222222222/orders", Scope: "aws/222222222222/eu-west-2"}
	var r Report
	r.Merge(&Report{Coverage: Coverage{NotInCloud: []NotInCloud{other, orders}}})
	// Two calls of one account and region that list the same service look
	// in the same root.
	r.Merge(&Report{Coverage: Coverage{NotInCloud: []NotInCloud{orders, jobs}}})

	r.Finish(nil, nil, false)

	if want := []NotInCloud{jobs, orders, other}; !reflect.DeepEqual(r.NotInCloud, want) {
		t.Errorf("not in the cloud:\ngot  %+v\nwant %+v", r.NotInCloud, want)
	}
	if r.ExitCode != ExitOK || r.Totals != (CoverageTotal{}) {
		t.Errorf("exit code %d, totals %+v; want 0 and nothing counted", r.ExitCode, r.Totals)
	}
	md := r.Markdown()
	for _, want := range []string{
		"\n## No longer in the cloud\n\n",
		"the next plan of the root would create them again. Delete each one's import block, if the root's `imports.tf` still has it, then either delete its resource block, and anything that refers to it, and plan; or, if it should exist, plan to create it again.",
		"- `aws_sqs_queue.jobs` `https://sqs.eu-west-2.amazonaws.com/111111111111/jobs` in `aws/111111111111/eu-west-2`\n" +
			"- `aws_sqs_queue.orders` `https://sqs.eu-west-2.amazonaws.com/111111111111/orders` in `aws/111111111111/eu-west-2`\n" +
			"- `aws_sqs_queue.orders` `https://sqs.eu-west-2.amazonaws.com/222222222222/orders` in `aws/222222222222/eu-west-2`\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}

	dir := t.TempDir()
	if err := r.WriteFiles(dir); err != nil {
		t.Fatal(err)
	}
	read, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read.NotInCloud, r.NotInCloud) {
		t.Errorf("coverage.json: got %+v, want %+v", read.NotInCloud, r.NotInCloud)
	}
	var out bytes.Buffer
	if err := r.WriteJSON(&out); err != nil {
		t.Fatal(err)
	}
	var got struct {
		NotInCloud []NotInCloud `json:"not_in_cloud"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got.NotInCloud, r.NotInCloud) {
		t.Errorf("JSON report: got %+v, %v\n%s", got.NotInCloud, err, out.String())
	}

	if md := (&Report{}).Markdown(); strings.Contains(md, "No longer in the cloud") {
		t.Errorf("a report with nothing gone has the section:\n%s", md)
	}
}

// The roots kept where they were are merged by path, from every call and
// child report that kept them, and listed in the report.
func TestKeptRoots(t *testing.T) {
	t.Parallel()
	r := &Report{KeptRoots: []KeptRoot{{Path: "aws/2/us-east-1", Environments: []string{"prod"}, Scope: "aws/2/us-east-1"}}}
	child := &Report{KeptRoots: []KeptRoot{
		{Path: "aws/1/us-east-1", Environments: []string{"shared", "dev"}, Scope: "aws/1/us-east-1"},
		{Path: "aws/1/us-east-1", Environments: []string{"dev", "a"}, Scope: "aws/1/us-east-1"},
	}}
	r.Merge(child)
	r.Finish(nil, nil, false)
	want := []KeptRoot{
		{Path: "aws/1/us-east-1", Environments: []string{"a", "dev", "shared"}, Scope: "aws/1/us-east-1"},
		{Path: "aws/2/us-east-1", Environments: []string{"prod"}, Scope: "aws/2/us-east-1"},
	}
	if !reflect.DeepEqual(r.KeptRoots, want) {
		t.Errorf("got %+v, want %+v", r.KeptRoots, want)
	}
	if got := child.KeptRoots[0].Environments; !reflect.DeepEqual(got, []string{"shared", "dev"}) {
		t.Errorf("the child's report changed: %v", got)
	}
	md := r.Markdown()
	if !strings.Contains(md, "## Kept where they were") || !strings.Contains(md, "- `aws/1/us-east-1`, of a, dev, shared") {
		t.Errorf("report.md:\n%s", md)
	}
	if strings.Contains((&Report{}).Markdown(), "Kept where they were") {
		t.Error("a report with no kept roots has the section")
	}
}
