// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/catalogue/schemameta"
)

// thingMetadata says what thingSchemas' provider code would: retries
// defaults to 3 and enabled to false.
func thingMetadata() *schemameta.File {
	return &schemameta.File{Provider: "registry.terraform.io/hashicorp/aws", Version: "6.70.0", Resources: map[string]*schemameta.Resource{
		"aws_thing": {Arguments: map[string]*schemameta.Argument{
			"retries": {Default: json.Number("3")},
			"enabled": {Default: false},
		}},
	}}
}

// exactHints are the hints of f for a root that uses its release.
func exactHints(f *schemameta.File) schemaHints {
	return schemaHints{file: f, exact: true, constrained: constrainedOf(f)}
}

// count returns how many of calls are call.
func count(calls []string, call string) int {
	n := 0
	for _, c := range calls {
		if c == call {
			n++
		}
	}
	return n
}

// With the provider's defaults known, retries = 0 is a setting from the
// start: TestStripDefaults plans twice to find that out, this once.
func TestStripDefaultsWithSchemaMetadata(t *testing.T) {
	t.Parallel()
	dir, changes := thingRoot(t)
	tf := &fakeTerraform{dir: dir, schemas: thingSchemas, showns: []*tfjson.Plan{importedPlan("aws_thing.a")}}

	stripped, last, err := stripDefaults(context.Background(), tf, dir, changeSummary{}, changes, nil, defaultRules{hints: exactHints(thingMetadata())})
	if err != nil || !stripped || last == nil {
		t.Fatalf("want defaults stripped and the plan handed back, got %v, %v, %v", stripped, last, err)
	}
	got := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{`name = "a"`, "computed = false", "retries = 0"} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"description", "enabled", "tags", "mode", "limit", "setting"} {
		if strings.Contains(got, gone) {
			t.Errorf("generated.tf still has %s:\n%s", gone, got)
		}
	}
	if calls := strings.Join(tf.calls, ","); calls != "schema,validate,plan,show" {
		t.Errorf("calls: %s", calls)
	}
	t.Logf("plans: 1 with the metadata, 2 without (TestStripDefaults)")
}

// Metadata of another release only suggests: stripDefaults plans as it
// does without any.
func TestStripDefaultsWithOtherReleaseMetadata(t *testing.T) {
	t.Parallel()
	dir, changes := thingRoot(t)
	first := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		resourceChange("aws_thing.a", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"retries": 0}, map[string]any{"retries": 3}),
	}}
	tf := &fakeTerraform{dir: dir, schemas: thingSchemas, showns: []*tfjson.Plan{first, importedPlan("aws_thing.a")}}
	hints := exactHints(thingMetadata())
	hints.exact = false

	if _, _, err := stripDefaults(context.Background(), tf, dir, changeSummary{}, changes, nil, defaultRules{hints: hints}); err != nil {
		t.Fatal(err)
	}
	if calls := strings.Join(tf.calls, ","); calls != "schema,validate,plan,show,validate,plan,show" {
		t.Errorf("calls: %s", calls)
	}
	if got := squashed(readFile(t, dir, GeneratedFileName)); !strings.Contains(got, "retries = 0") {
		t.Errorf("generated.tf lost retries = 0:\n%s", got)
	}
}

// Nulls and proven defaults alone need no validation, and a plan that
// differs on the resource can't be about them.
func TestStripDefaultsProvenDefaultsNeedNoValidation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_thing" "a" {
  name        = "a"
  description = null
  enabled     = false
  retries     = 3
}
`)
	content, err := ImportsFile([]Import{{Type: "aws_thing", Name: "a", ID: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, ImportsFileName, string(content))
	changes := map[string]string{"aws_thing.a": changeSignature(resourceChange("aws_thing.a", tfjson.Actions{tfjson.ActionNoop}, nil, nil))}
	// As an emulator's plan may: a change that leaving out defaults
	// can't have made.
	noisy := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		resourceChange("aws_thing.a", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"name": "a"}, map[string]any{"name": "b"}),
	}}
	tf := &fakeTerraform{dir: dir, schemas: thingSchemas, showns: []*tfjson.Plan{noisy}}

	stripped, _, err := stripDefaults(context.Background(), tf, dir, changeSummary{}, changes, nil, defaultRules{hints: exactHints(thingMetadata())})
	if err != nil || !stripped {
		t.Fatalf("want defaults stripped, got %v, %v", stripped, err)
	}
	if calls := strings.Join(tf.calls, ","); calls != "schema,plan,show" {
		t.Errorf("calls: %s", calls)
	}
	if got := squashed(readFile(t, dir, GeneratedFileName)); got != `resource "aws_thing" "a" { name = "a" }` {
		t.Errorf("generated.tf:\n%s", got)
	}
}

// Where the plan can't settle anything, proven defaults still go, as nulls
// do, once validation agrees.
func TestStripDefaultsKeepsProvenDefaultsOutOnErrors(t *testing.T) {
	t.Parallel()
	dir, changes := thingRoot(t)
	tf := &fakeTerraform{dir: dir, schemas: thingSchemas, plans: []fakePlan{{diags: []tfjson.Diagnostic{{Severity: tfjson.DiagnosticSeverityError, Summary: "Invalid provider configuration"}}}}}

	stripped, _, err := stripDefaults(context.Background(), tf, dir, changeSummary{}, changes, nil, defaultRules{hints: exactHints(thingMetadata())})
	if err != nil || !stripped {
		t.Fatalf("want the nulls and proven defaults stripped, got %v, %v", stripped, err)
	}
	got := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{"retries = 0", "tags = {}", `mode = ""`} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"null", "enabled"} {
		if strings.Contains(got, gone) {
			t.Errorf("generated.tf still has %s:\n%s", gone, got)
		}
	}
	if calls := strings.Join(tf.calls, ","); !strings.HasSuffix(calls, ",validate") {
		t.Errorf("want a validation of what is left out last, got calls: %s", calls)
	}
}

func TestSchemaHints(t *testing.T) {
	t.Parallel()
	meta := &schemameta.File{Provider: "p", Version: "1.0.0", Resources: map[string]*schemameta.Resource{
		"sdk": {Arguments: map[string]*schemameta.Argument{
			"retries":         {Default: json.Number("3")},
			"mode":            {Default: "auto"},
			"lower":           {Default: "auto", Suppress: true},
			"block.ratio":     {Default: json.Number("0.5")},
			"one":             {Default: false, AtLeastOneOf: []string{"one", "other"}},
			"other":           {Default: true},
			"needs":           {RequiredWith: []string{"needed"}},
			"needed":          {Default: json.Number("1")},
			"unconstrained":   {Default: true},
			"string_not_bool": {Default: "true"},
		}},
		"fw": {Framework: true, Arguments: map[string]*schemameta.Argument{
			"mode": {Default: "auto"},
		}},
	}}
	exact := exactHints(meta)
	other := exactHints(meta)
	other.exact = false
	for _, tc := range []struct {
		typ, path, value      string
		proven, setting, fwok bool
		hints                 schemaHints
	}{
		{"sdk", "retries", "3", true, false, false, exact},
		{"sdk", "retries", "3.0", true, false, false, exact},
		{"sdk", "retries", "0", false, true, false, exact},
		{"sdk", "retries", "3", false, false, false, other},
		{"sdk", "mode", `"auto"`, true, false, false, exact},
		{"sdk", "mode", `"manual"`, false, true, false, exact},
		{"sdk", "lower", `"AUTO"`, false, false, false, exact},
		{"sdk", "block.ratio", "0.5", true, false, false, exact},
		// Named by a constraint, on either side.
		{"sdk", "one", "false", false, false, false, exact},
		{"sdk", "other", "true", false, false, false, exact},
		{"sdk", "needed", "1", false, false, false, exact},
		{"sdk", "unconstrained", "true", true, false, false, exact},
		{"sdk", "string_not_bool", "true", false, true, false, exact},
		{"sdk", "mode", "var.mode", false, false, false, exact},
		{"sdk", "unknown", "1", false, false, false, exact},
		{"fw", "mode", `"auto"`, false, false, true, exact},
		{"fw", "mode", `"auto"`, false, false, true, other},
		{"fw", "mode", `"manual"`, false, false, false, exact},
	} {
		expr, diags := hclsyntax.ParseExpression([]byte(tc.value), "x.tf", hcl.InitialPos)
		if diags.HasErrors() {
			t.Fatal(diags)
		}
		path := strings.Split(tc.path, ".")
		if got := tc.hints.provenDefault(tc.typ, path, expr); got != tc.proven {
			t.Errorf("provenDefault(%s, %s, %s) = %v (exact %v)", tc.typ, tc.path, tc.value, got, tc.hints.exact)
		}
		if got := tc.hints.provenSetting(tc.typ, path, expr); got != tc.setting {
			t.Errorf("provenSetting(%s, %s, %s) = %v (exact %v)", tc.typ, tc.path, tc.value, got, tc.hints.exact)
		}
		if got := tc.hints.frameworkDefault(tc.typ, path, expr); got != tc.fwok {
			t.Errorf("frameworkDefault(%s, %s, %s) = %v", tc.typ, tc.path, tc.value, got)
		}
	}
	// No metadata knows nothing.
	expr, _ := hclsyntax.ParseExpression([]byte("3"), "x.tf", hcl.InitialPos)
	if (schemaHints{}).provenDefault("sdk", []string{"retries"}, expr) || (schemaHints{}).provenSetting("sdk", []string{"retries"}, expr) {
		t.Error("empty hints prove something")
	}
}

// A Plugin Framework resource's computed argument set to its default is
// a candidate the plan checks; other computed constants stay.
func TestRemoveDefaultsFrameworkDefaults(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, t.TempDir(), GeneratedFileName, `resource "aws_fw" "a" {
  mode  = "auto"
  other = "auto"
}
`)
	schemas := &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
		"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
			"aws_fw": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
				"mode":  {AttributeType: cty.String, Optional: true, Computed: true},
				"other": {AttributeType: cty.String, Optional: true, Computed: true},
			}}},
		}},
	}}
	meta := &schemameta.File{Provider: "registry.terraform.io/hashicorp/aws", Version: "6.70.0", Resources: map[string]*schemameta.Resource{
		"aws_fw": {Framework: true, Arguments: map[string]*schemameta.Argument{"mode": {Default: "auto"}}},
	}}
	removed := removeDefaults(f, schemas, defaultRules{hints: schemaHints{file: meta}}, nil)
	if len(removed) != 1 || removed[0].key() != "aws_fw.a\x00mode" || removed[0].proven {
		t.Errorf("removed %+v, want only mode, unproven", removed)
	}
}

func TestSchemaHintsFor(t *testing.T) {
	t.Parallel()
	meta := thingMetadata()
	lock := func(version string) string {
		dir := t.TempDir()
		writeConfig(t, dir, LockFileName, `provider "registry.terraform.io/hashicorp/aws" {
  version     = "`+version+`"
  constraints = "~> 6.70"
}
`)
		return dir
	}
	if h := schemaHintsFor(lock("6.70.0"), meta); !h.exact || h.file != meta {
		t.Errorf("the release the lock records: want exact hints, got %+v", h)
	}
	if h := schemaHintsFor(lock("6.71.0"), meta); h.exact || h.file != meta {
		t.Errorf("another release: want hints only, got %+v", h)
	}
	if h := schemaHintsFor(t.TempDir(), meta); h.exact {
		t.Errorf("no lock file: want hints only, got %+v", h)
	}
	if h := schemaHintsFor(t.TempDir(), nil); h.file != nil || h.exact {
		t.Errorf("no metadata: want none, got %+v", h)
	}
}

// subnetMetadata says, as the AWS provider's code does, that a subnet's
// zone is set by name or by ID, not both, and a load balancer's subnets by
// list or by mapping blocks.
func subnetMetadata() *schemameta.File {
	return &schemameta.File{Provider: "registry.terraform.io/hashicorp/aws", Version: "6.70.0", Resources: map[string]*schemameta.Resource{
		"aws_subnet": {Arguments: map[string]*schemameta.Argument{
			"availability_zone":    {ConflictsWith: []string{"availability_zone_id"}},
			"availability_zone_id": {ConflictsWith: []string{"availability_zone"}},
		}},
		"aws_kinesis_stream": {Arguments: map[string]*schemameta.Argument{
			"shard_count":            {ConflictsWith: []string{"warm_throughput_mib_ps"}},
			"warm_throughput_mib_ps": {ConflictsWith: []string{"shard_count"}},
		}},
		"aws_lb": {Arguments: map[string]*schemameta.Argument{
			"subnets":        {ExactlyOneOf: []string{"subnet_mapping", "subnets"}},
			"subnet_mapping": {ExactlyOneOf: []string{"subnet_mapping", "subnets"}},
		}},
		"aws_route": {Arguments: map[string]*schemameta.Argument{
			"target.gateway_id":     {ConflictsWith: []string{"target.nat_gateway_id"}},
			"target.nat_gateway_id": {ConflictsWith: []string{"target.gateway_id"}},
		}},
	}}
}

// The metadata settles the conflicts TestRemoveRejected and
// TestRemoveRejectedBlocksAndObjects have validation report, the same way:
// the later of two attributes goes, or the zero one, and an attribute
// gives way to blocks; in nested blocks too.
func TestResolveConflicts(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), GeneratedFileName)
	config := rejectedConfig + "\n" + conflictingBlocksConfig + `
resource "aws_route" "e" {
  target {
    gateway_id     = "igw-1"
    nat_gateway_id = "nat-1"
  }
}

resource "aws_subnet" "f" {
  availability_zone    = "us-east-1a"
  availability_zone_id = null
}
`
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := resolveConflicts(path, exactHints(subnetMetadata()))
	if err != nil || !changed {
		t.Fatalf("want a change, got changed=%v err=%v", changed, err)
	}

	got := squashed(readFile(t, filepath.Dir(path), GeneratedFileName))
	for _, removed := range []string{`availability_zone_id = "us-east-1-az1"`, "warm_throughput_mib_ps", "subnets =", "nat_gateway_id"} {
		if strings.Contains(got, removed) {
			t.Errorf("%s not removed:\n%s", removed, got)
		}
	}
	for _, kept := range []string{`availability_zone = "us-east-1a"`, "shard_count = 1", "subnet_mapping {", `gateway_id = "igw-1"`, "availability_zone_id = null", "enable_lni_at_device_index = 0"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%s not kept:\n%s", kept, got)
		}
	}
	// Settled, nothing more conflicts.
	if changed, err := resolveConflicts(path, exactHints(subnetMetadata())); err != nil || changed {
		t.Errorf("second pass: changed=%v err=%v", changed, err)
	}
}

// subnetLock is the lock file of a root that uses the release
// subnetMetadata is of.
const subnetLock = `provider "registry.terraform.io/hashicorp/aws" {
  version = "6.70.0"
}
`

// Metadata of another release settles no conflicts: validation and repair
// judge them, as without metadata.
func TestResolveConflictsNeedsTheExactRelease(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), GeneratedFileName)
	if err := os.WriteFile(path, []byte(rejectedConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	hints := exactHints(subnetMetadata())
	hints.exact = false
	if changed, err := resolveConflicts(path, hints); err != nil || changed {
		t.Errorf("changed=%v err=%v, want no change", changed, err)
	}
}

// generatedSubnets is what Terraform generates for two subnets, in the
// order its graph walk reached them, with the zone by name and by ID.
const generatedSubnets = `resource "aws_subnet" "b" {
  availability_zone    = "us-east-1b"
  availability_zone_id = "use1-az2"
  vpc_id               = "vpc-1"
}

resource "aws_subnet" "a" {
  availability_zone    = "us-east-1a"
  availability_zone_id = "use1-az1"
  vpc_id               = "vpc-1"
}
`

// With the metadata, Generate settles the conflicting arguments before it
// plans the sorted configuration again: no plan fails on them, and no
// validation needs to say so.
func TestGenerateResolvesConflictsBeforeValidation(t *testing.T) {
	t.Parallel()
	conflict := "Conflicting configuration arguments"
	// The unsorted configuration has them on the same lines as the sorted
	// one: both subnets look alike.
	diags := []tfjson.Diagnostic{
		errorAt(2, conflict, `"availability_zone": conflicts with availability_zone_id`),
		errorAt(3, conflict, `"availability_zone_id": conflicts with availability_zone`),
		errorAt(8, conflict, `"availability_zone": conflicts with availability_zone_id`),
		errorAt(9, conflict, `"availability_zone_id": conflicts with availability_zone`),
	}
	imports := []Import{{Type: "aws_subnet", Name: "a", ID: "subnet-a"}, {Type: "aws_subnet", Name: "b", ID: "subnet-b"}}
	run := func(t *testing.T, meta *schemameta.File) []string {
		t.Helper()
		dir := t.TempDir()
		tf := &fakeTerraform{dir: dir, generated: generatedSubnets, plans: []fakePlan{{diags: diags}}}
		// init records the release the metadata is of.
		tf.onInit = func() {
			if err := os.WriteFile(filepath.Join(dir, LockFileName), []byte(subnetLock), 0o644); err != nil {
				t.Error(err)
			}
		}
		if meta == nil {
			// Without it, the sorted configuration's plan and validation
			// report the conflicts, and repair settles them.
			tf.plans = append(tf.plans, fakePlan{diags: diags})
			tf.validations = []*tfjson.ValidateOutput{{Diagnostics: diags}}
		}
		result, err := Generate(context.Background(), tf, dir, imports, Options{SchemaMetadata: meta})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Rejected) != 0 {
			t.Errorf("want nothing left out, got %+v", result.Rejected)
		}
		got := readFile(t, dir, GeneratedFileName)
		if strings.Contains(got, "availability_zone_id") || strings.Count(got, "availability_zone ") != 2 {
			t.Errorf("want the zone IDs left out, the names kept:\n%s", got)
		}
		return tf.calls
	}
	before, after := run(t, nil), run(t, subnetMetadata())
	t.Logf("terraform plan: %d without the metadata, %d with it; terraform validate: %d, %d", count(before, "plan"), count(after, "plan"), count(before, "validate"), count(after, "validate"))
	// The plan of the sorted configuration no longer fails, so neither repair's
	// two validations nor the plan after it are needed.
	if count(before, "plan")-count(after, "plan") != 1 || count(before, "validate")-count(after, "validate") != 2 {
		t.Errorf("want one plan and two validations fewer with the metadata:\nwithout: %v\nwith:    %v", before, after)
	}
}
