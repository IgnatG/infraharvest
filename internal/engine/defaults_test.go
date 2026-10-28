// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

const withDefaults = `resource "aws_thing" "a" {
  name        = "a"
  description = null
  enabled     = false
  computed    = false
  retries     = 0
  tags        = {}
  setting {
    mode  = ""
    limit = null
  }
}
`

var thingSchemas = &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
	"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
		"aws_thing": {Block: &tfjson.SchemaBlock{
			Attributes: map[string]*tfjson.SchemaAttribute{
				"name":        {AttributeType: cty.String, Required: true},
				"description": {AttributeType: cty.String, Optional: true},
				"enabled":     {AttributeType: cty.Bool, Optional: true},
				"computed":    {AttributeType: cty.Bool, Optional: true, Computed: true},
				"retries":     {AttributeType: cty.Number, Optional: true},
				"tags":        {AttributeType: cty.Map(cty.String), Optional: true},
			},
			NestedBlocks: map[string]*tfjson.SchemaBlockType{"setting": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
				"mode":  {AttributeType: cty.String, Optional: true},
				"limit": {AttributeType: cty.Number, Optional: true},
			}}}},
		}},
	}},
}}

func thingRoot(t *testing.T) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, withDefaults)
	content, err := rootfile.ImportsFile([]rootfile.Import{{Type: "aws_thing", Name: "a", ID: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, rootfile.ImportsFileName, string(content))
	return dir, map[string]string{"aws_thing.a": tfrun.ChangeSignature(resourceChange("aws_thing.a", tfjson.Actions{tfjson.ActionNoop}, nil, nil))}
}

// Nulls, constant defaults and the blocks they leave empty go; a constant
// that isn't the default, as the plan shows, and computed arguments stay.
func TestStripDefaults(t *testing.T) {
	t.Parallel()
	dir, changes := thingRoot(t)
	// Without retries = 0, the provider would use its own default.
	first := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		resourceChange("aws_thing.a", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"retries": 0}, map[string]any{"retries": 3}),
	}}
	tf := &fakeTerraform{dir: dir, schemas: thingSchemas, showns: []*tfjson.Plan{first, importedPlan("aws_thing.a")}}

	stripped, d, err := settleDefaults(tf, dir, changes, defaultRules{})
	if err != nil || !stripped {
		t.Fatalf("want defaults stripped, got %v, %v", stripped, err)
	}
	// The second plan showed generated.tf as it is left: the passes after
	// compare with it rather than plan again (see the calls below).
	if got := d.baseline.Planned("aws_thing.a"); got != "no-op:" {
		t.Errorf("want the last plan the baseline, got %q", got)
	}

	got := squashed(readFile(t, dir, rootfile.GeneratedFileName))
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
	if calls := strings.Join(tf.calls, ","); calls != "schema,validate,plan,show,validate,plan,show" {
		t.Errorf("calls: %s", calls)
	}
}

// An empty block whose absence the plan shows stays.
func TestStripDefaultsKeepsEmptyBlocksThePlanNeeds(t *testing.T) {
	t.Parallel()
	dir, changes := thingRoot(t)
	first := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		resourceChange("aws_thing.a", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"setting": []any{map[string]any{}}}, map[string]any{"setting": []any{}}),
	}}
	tf := &fakeTerraform{dir: dir, schemas: thingSchemas, showns: []*tfjson.Plan{first, importedPlan("aws_thing.a")}}

	if _, _, err := settleDefaults(tf, dir, changes, defaultRules{}); err != nil {
		t.Fatal(err)
	}
	if got := squashed(readFile(t, dir, rootfile.GeneratedFileName)); !strings.Contains(got, "setting {") {
		t.Errorf("generated.tf lost the setting block:\n%s", got)
	}
}

// An error the plan can't pin on a resource keeps every constant; nulls
// still go, as they are the same as leaving the argument out.
func TestStripDefaultsUndoesOnErrors(t *testing.T) {
	t.Parallel()
	dir, changes := thingRoot(t)
	tf := &fakeTerraform{dir: dir, schemas: thingSchemas, plans: []fakePlan{{diags: []tfjson.Diagnostic{{Severity: tfjson.DiagnosticSeverityError, Summary: "Invalid provider configuration"}}}}}

	stripped, _, err := settleDefaults(tf, dir, changes, defaultRules{})
	if err != nil || !stripped {
		t.Fatalf("want the nulls stripped, got %v, %v", stripped, err)
	}
	// No plan showed the configuration with only the nulls left out: the
	// pass plans it, for the passes after to compare with.
	if got := strings.Join(tf.calls[len(tf.calls)-2:], ","); got != "plan,show" {
		t.Errorf("calls: %v, want a plan of what is left at the end", tf.calls)
	}
	got := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	for _, want := range []string{"enabled = false", "retries = 0", "tags = {}", `mode = ""`} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "null") {
		t.Errorf("generated.tf still has nulls:\n%s", got)
	}
}

// A Google resource's labels, which an import leaves out (see
// EffectiveTags), are never a default: leaving them out would plan the
// same, as the labels stay on the resource, no longer managed. Other
// constants still go.
func TestRemoveDefaultsKeepsTags(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, t.TempDir(), rootfile.GeneratedFileName, `resource "google_pubsub_topic" "events" {
  name   = "events"
  labels = { env = "prod" }
  kms_key_name = "k"
}
`)
	schemas := &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
		"registry.terraform.io/hashicorp/google": {ResourceSchemas: map[string]*tfjson.Schema{
			"google_pubsub_topic": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
				"name":         {AttributeType: cty.String, Required: true},
				"labels":       {AttributeType: cty.Map(cty.String), Optional: true},
				"kms_key_name": {AttributeType: cty.String, Optional: true},
			}}},
		}},
	}}
	removed := removeDefaults(f, schemas, defaultRules{tags: "labels"}, nil)
	if len(removed) != 1 || removed[0].key() != "google_pubsub_topic.events\x00kms_key_name" {
		t.Errorf("removed %v, want only kms_key_name", removed)
	}
	if got := squashed(string(f.Edit().Bytes())); !strings.Contains(got, `labels = { env = "prod" }`) {
		t.Errorf("the labels are left out:\n%s", got)
	}
}

var roleSchemas = &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
	"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
		"aws_role": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
			"name":                 {AttributeType: cty.String, Required: true},
			"description":          {AttributeType: cty.String, Optional: true},
			"max_session_duration": {AttributeType: cty.Number, Optional: true},
			"path":                 {AttributeType: cty.String, Optional: true},
			"managed":              {AttributeType: cty.String, Optional: true, Computed: true},
			"alpha":                {AttributeType: cty.String, Optional: true},
			"beta":                 {AttributeType: cty.String, Optional: true},
			"gamma":                {AttributeType: cty.Number, Optional: true},
		}}},
	}},
}}

// roleRoot is a root with one aws_role, generated as content, which plans
// with no change.
func roleRoot(t *testing.T, content string) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, content)
	imports, err := rootfile.ImportsFile([]rootfile.Import{{Type: "aws_role", Name: "a", ID: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, rootfile.ImportsFileName, string(imports))
	return dir, map[string]string{"aws_role.a": tfrun.ChangeSignature(resourceChange("aws_role.a", tfjson.Actions{tfjson.ActionNoop}, nil, nil))}
}

// Any constant can be an optional argument's default, not only false, 0
// and "": the plan tells which are, and the others stay. Computed
// arguments stay whatever their value.
func TestStripDefaultsLeavesOutConstantDefaults(t *testing.T) {
	t.Parallel()
	dir, changes := roleRoot(t, `resource "aws_role" "a" {
  description          = "the app's role"
  managed              = "x"
  max_session_duration = 3600
  name                 = "app"
  path                 = "/"
}
`)
	first := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		resourceChange("aws_role.a", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"description": "the app's role"}, map[string]any{"description": nil}),
	}}
	tf := &fakeTerraform{dir: dir, schemas: roleSchemas, showns: []*tfjson.Plan{first, importedPlan("aws_role.a")}}

	stripped, _, err := settleDefaults(tf, dir, changes, defaultRules{})
	if err != nil || !stripped {
		t.Fatalf("want defaults stripped, got %v, %v", stripped, err)
	}
	got := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	for _, want := range []string{`description = "the app's role"`, `managed = "x"`, `name = "app"`} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"max_session_duration", "path"} {
		if strings.Contains(got, gone) {
			t.Errorf("generated.tf still has %s:\n%s", gone, got)
		}
	}
	// One plan for all the arguments, one more for those put back.
	if calls := strings.Join(tf.calls, ","); calls != "schema,validate,plan,show,validate,plan,show" {
		t.Errorf("calls: %s", calls)
	}
}

// What validation rejects once the defaults are out, such as one of two
// required alternatives, comes back before any plan: the arguments the
// errors name, and the rest of the resource's constants still go.
func TestStripDefaultsKeepsWhatValidationRejects(t *testing.T) {
	t.Parallel()
	dir, changes := roleRoot(t, `resource "aws_role" "a" {
  alpha = "a"
  beta  = "b"
  gamma = 1
  name  = "app"
}
`)
	rejected := errorAt(1, "Invalid combination of arguments", `"alpha": one of `+"`alpha,beta`"+` must be specified`)
	tf := &fakeTerraform{
		dir:         dir,
		schemas:     roleSchemas,
		validations: []*tfjson.ValidateOutput{{Diagnostics: []tfjson.Diagnostic{rejected}}},
		showns:      []*tfjson.Plan{importedPlan("aws_role.a")},
	}

	stripped, _, err := settleDefaults(tf, dir, changes, defaultRules{})
	if err != nil || !stripped {
		t.Fatalf("want defaults stripped, got %v, %v", stripped, err)
	}
	got := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	for _, want := range []string{`alpha = "a"`, `beta = "b"`, `name = "app"`} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "gamma") {
		t.Errorf("generated.tf still has gamma:\n%s", got)
	}
	if calls := strings.Join(tf.calls, ","); calls != "schema,validate,validate,plan,show" {
		t.Errorf("calls: %s", calls)
	}
}

// A resource that plans differently a second time, for another argument,
// keeps the arguments behind each difference and leaves the rest out.
func TestStripDefaultsRetriesTheRestOfAResourceBlamedTwice(t *testing.T) {
	t.Parallel()
	dir, changes := roleRoot(t, `resource "aws_role" "a" {
  alpha = "a"
  beta  = "b"
  gamma = 1
  name  = "app"
}
`)
	alpha := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		resourceChange("aws_role.a", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"alpha": "a"}, map[string]any{"alpha": nil}),
	}}
	beta := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		resourceChange("aws_role.a", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"beta": "b"}, map[string]any{"beta": nil}),
	}}
	tf := &fakeTerraform{dir: dir, schemas: roleSchemas, showns: []*tfjson.Plan{alpha, beta, importedPlan("aws_role.a")}}

	stripped, _, err := settleDefaults(tf, dir, changes, defaultRules{})
	if err != nil || !stripped {
		t.Fatalf("want defaults stripped, got %v, %v", stripped, err)
	}
	got := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	for _, want := range []string{`alpha = "a"`, `beta = "b"`, `name = "app"`} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "gamma") {
		t.Errorf("generated.tf still has gamma:\n%s", got)
	}
	if calls := strings.Join(tf.calls, ","); calls != "schema,validate,plan,show,validate,plan,show,validate,plan,show" {
		t.Errorf("calls: %s", calls)
	}
}

// A resource that keeps planning differently keeps all its constants in
// the last round but one, so that the plans stay few.
func TestStripDefaultsKeepsEverythingOfAResourceInTheLastRounds(t *testing.T) {
	t.Parallel()
	dir, changes := roleRoot(t, `resource "aws_role" "a" {
  alpha       = "a"
  beta        = "b"
  description = "d"
  gamma       = 1
  name        = "app"
  path        = "/"
}
`)
	differs := func(attribute string, value any) *tfjson.Plan {
		return &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
			resourceChange("aws_role.a", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{attribute: value}, map[string]any{attribute: nil}),
		}}
	}
	tf := &fakeTerraform{dir: dir, schemas: roleSchemas, showns: []*tfjson.Plan{differs("alpha", "a"), differs("beta", "b"), differs("description", "d"), differs("gamma", 1)}}

	stripped, _, err := settleDefaults(tf, dir, changes, defaultRules{})
	if err != nil {
		t.Fatal(err)
	}
	got := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	for _, want := range []string{`alpha = "a"`, `beta = "b"`, `description = "d"`, "gamma = 1", `path = "/"`} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, got)
		}
	}
	if stripped {
		t.Error("nothing was left out, yet stripDefaults reports a change")
	}
	if calls := strings.Join(tf.calls, ","); calls != "schema"+strings.Repeat(",validate,plan,show", 4) {
		t.Errorf("calls: %s", calls)
	}
}

// Checks a provider makes when it plans come one at a time, such as a
// metric alarm's comparison_operator, then evaluation_periods: after the
// arguments the first error names, the second keeps the resource's
// settings, and its empty and zero values still go.
func TestStripDefaultsKeepsSettingsWhenThePlanRejectsAgain(t *testing.T) {
	t.Parallel()
	dir, changes := roleRoot(t, `resource "aws_role" "a" {
  alpha                = "a"
  beta                 = "b"
  description          = ""
  gamma                = 1
  max_session_duration = 0
  name                 = "app"
}
`)
	tf := &fakeTerraform{
		dir:     dir,
		schemas: roleSchemas,
		plans: []fakePlan{
			{diags: []tfjson.Diagnostic{errorAt(1, "alpha is required for this role", "")}},
			{diags: []tfjson.Diagnostic{errorAt(1, "beta is required for this role", "")}},
		},
		showns: []*tfjson.Plan{importedPlan("aws_role.a")},
	}

	stripped, _, err := settleDefaults(tf, dir, changes, defaultRules{})
	if err != nil || !stripped {
		t.Fatalf("want defaults stripped, got %v, %v", stripped, err)
	}
	got := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	for _, want := range []string{`alpha = "a"`, `beta = "b"`, "gamma = 1", `name = "app"`} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"description", "max_session_duration"} {
		if strings.Contains(got, gone) {
			t.Errorf("generated.tf still has %s:\n%s", gone, got)
		}
	}
	if calls := strings.Join(tf.calls, ","); calls != "schema,validate,plan,validate,plan,validate,plan,show" {
		t.Errorf("calls: %s", calls)
	}
}

var lambdaSchemas = &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
	"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
		"aws_lambda_function": {Block: &tfjson.SchemaBlock{
			Attributes: map[string]*tfjson.SchemaAttribute{
				"filename":      {AttributeType: cty.String, Optional: true},
				"function_name": {AttributeType: cty.String, Required: true},
				"handler":       {AttributeType: cty.String, Optional: true},
				"image_uri":     {AttributeType: cty.String, Optional: true},
				"layers":        {AttributeType: cty.List(cty.String), Optional: true},
				"memory_size":   {AttributeType: cty.Number, Optional: true},
				"package_type":  {AttributeType: cty.String, Optional: true},
				"role":          {AttributeType: cty.String, Required: true},
				"runtime":       {AttributeType: cty.String, Optional: true},
				"s3_bucket":     {AttributeType: cty.String, Optional: true},
				"skip_destroy":  {AttributeType: cty.Bool, Optional: true},
				"timeout":       {AttributeType: cty.Number, Optional: true},
			},
			NestedBlocks: map[string]*tfjson.SchemaBlockType{"ephemeral_storage": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
				"size": {AttributeType: cty.Number, Optional: true, Computed: true},
			}}}},
		}},
	}},
}}

// An error of the plan that validation doesn't see, such as the AWS
// provider's "handler and runtime must be set when PackageType is Zip",
// keeps the arguments it names, and the resource's other defaults still
// go. Before, a Lambda function kept every constant: memory_size = 128,
// timeout = 3, layers = [] and the rest.
func TestStripDefaultsKeepsWhatThePlanRejects(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "aws_lambda_function" "a" {
  filename      = "lambda/a.zip"
  function_name = "a"
  handler       = "index.handler"
  layers        = []
  memory_size   = 128
  package_type  = "Zip"
  role          = "app-role"
  runtime       = "nodejs22.x"
  skip_destroy  = false
  timeout       = 3
  ephemeral_storage {
    size = 512
  }
  lifecycle {
    ignore_changes = [filename]
  }
}
`)
	imports, err := rootfile.ImportsFile([]rootfile.Import{{Type: "aws_lambda_function", Name: "a", ID: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, rootfile.ImportsFileName, string(imports))
	changes := map[string]string{"aws_lambda_function.a": tfrun.ChangeSignature(resourceChange("aws_lambda_function.a", tfjson.Actions{tfjson.ActionNoop}, nil, nil))}
	alternatives := errorAt(1, "Invalid combination of arguments", `"filename": one of `+"`filename,image_uri,s3_bucket`"+` must be specified`)
	tf := &fakeTerraform{
		dir:         dir,
		schemas:     lambdaSchemas,
		validations: []*tfjson.ValidateOutput{{Diagnostics: []tfjson.Diagnostic{alternatives}}},
		plans:       []fakePlan{{diags: []tfjson.Diagnostic{errorAt(1, "handler and runtime must be set when PackageType is Zip", "")}}},
		showns:      []*tfjson.Plan{importedPlan("aws_lambda_function.a")},
	}
	rules := defaultRules{defaults: map[string][]string{"aws_lambda_function": {"ephemeral_storage.size=512", "memory_size=128", "package_type=Zip", "timeout=3"}}}

	stripped, _, err := settleDefaults(tf, dir, changes, rules)
	if err != nil || !stripped {
		t.Fatalf("want defaults stripped, got %v, %v", stripped, err)
	}
	got := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	for _, want := range []string{`filename = "lambda/a.zip"`, `handler = "index.handler"`, `runtime = "nodejs22.x"`, `role = "app-role"`, "ignore_changes = [filename]"} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"layers", "memory_size", "package_type", "skip_destroy", "timeout", "ephemeral_storage"} {
		if strings.Contains(got, gone) {
			t.Errorf("generated.tf still has %s:\n%s", gone, got)
		}
	}
	if calls := strings.Join(tf.calls, ","); calls != "schema,validate,validate,plan,validate,plan,show" {
		t.Errorf("calls: %s", calls)
	}
}

var lbSchemas = &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
	"registry.terraform.io/hashicorp/aws": {ResourceSchemas: map[string]*tfjson.Schema{
		"aws_lb": {Block: &tfjson.SchemaBlock{
			Attributes: map[string]*tfjson.SchemaAttribute{
				"name":            {AttributeType: cty.String, Required: true},
				"description":     {AttributeType: cty.String, Optional: true},
				"route":           {AttributeType: cty.List(cty.String), Optional: true, Computed: true},
				"security_groups": {AttributeType: cty.Set(cty.String), Optional: true, Computed: true},
			},
			NestedBlocks: map[string]*tfjson.SchemaBlockType{
				"access_logs": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
					"bucket":  {AttributeType: cty.String, Required: true},
					"enabled": {AttributeType: cty.Bool, Optional: true},
				}}},
			},
		}},
	}},
}}

// Empty values go whether or not the provider computes the argument, but
// for those the catalogue keeps; so do optional blocks of zero values as a
// whole, even with a required argument set to "".
func TestStripDefaultsLeavesOutEmptyValues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "aws_lb" "a" {
  description     = ""
  name            = "web"
  route           = []
  security_groups = []
  access_logs {
    bucket  = ""
    enabled = false
  }
}
`)
	imports, err := rootfile.ImportsFile([]rootfile.Import{{Type: "aws_lb", Name: "a", ID: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, rootfile.ImportsFileName, string(imports))
	changes := map[string]string{"aws_lb.a": tfrun.ChangeSignature(resourceChange("aws_lb.a", tfjson.Actions{tfjson.ActionNoop}, nil, nil))}
	tf := &fakeTerraform{dir: dir, schemas: lbSchemas}

	stripped, _, err := settleDefaults(tf, dir, changes, defaultRules{keepEmpty: map[string][]string{"aws_lb": {"route"}}})
	if err != nil || !stripped {
		t.Fatalf("want empty values stripped, got %v, %v", stripped, err)
	}
	got := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	for _, want := range []string{`name = "web"`, "route = []"} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"description", "security_groups", "access_logs", "bucket"} {
		if strings.Contains(got, gone) {
			t.Errorf("generated.tf still has %s:\n%s", gone, got)
		}
	}
	if calls := strings.Join(tf.calls, ","); calls != "schema,validate,plan,show" {
		t.Errorf("calls: %s", calls)
	}
}

// The catalogue's defaults go too, computed or not, by their path in
// nested blocks, and a block they leave empty with them; another value
// stays, and so does a default the plan shows isn't one. Before, a
// bucket kept storage_class = "STANDARD" and
// soft_delete_policy { retention_duration_seconds = 604800 }.
func TestStripDefaultsNamedInTheCatalogue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, rootfile.GeneratedFileName, `resource "google_storage_bucket" "a" {
  name          = "a"
  storage_class = "STANDARD"
  rpo           = "DEFAULT"
  location      = "EU"
  soft_delete_policy {
    retention_duration_seconds = 604800
  }
}

resource "google_storage_bucket" "b" {
  name          = "b"
  storage_class = "NEARLINE"
  soft_delete_policy {
    retention_duration_seconds = 0
  }
}
`)
	content, err := rootfile.ImportsFile([]rootfile.Import{{Type: "google_storage_bucket", Name: "a", ID: "a"}, {Type: "google_storage_bucket", Name: "b", ID: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, rootfile.ImportsFileName, string(content))
	noChange := tfrun.ChangeSignature(resourceChange("x", tfjson.Actions{tfjson.ActionNoop}, nil, nil))
	changes := map[string]string{"google_storage_bucket.a": noChange, "google_storage_bucket.b": noChange}
	schemas := &tfjson.ProviderSchemas{Schemas: map[string]*tfjson.ProviderSchema{
		"registry.terraform.io/hashicorp/google": {ResourceSchemas: map[string]*tfjson.Schema{
			"google_storage_bucket": {Block: &tfjson.SchemaBlock{
				Attributes: map[string]*tfjson.SchemaAttribute{
					"name":          {AttributeType: cty.String, Required: true},
					"location":      {AttributeType: cty.String, Required: true},
					"storage_class": {AttributeType: cty.String, Optional: true},
					"rpo":           {AttributeType: cty.String, Optional: true, Computed: true},
				},
				NestedBlocks: map[string]*tfjson.SchemaBlockType{"soft_delete_policy": {Block: &tfjson.SchemaBlock{Attributes: map[string]*tfjson.SchemaAttribute{
					"retention_duration_seconds": {AttributeType: cty.Number, Optional: true, Computed: true},
				}}}},
			}},
		}},
	}}
	// Without rpo, the plan would change a's: its default is another.
	first := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		resourceChange("google_storage_bucket.a", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"rpo": "DEFAULT"}, map[string]any{"rpo": nil}),
		// And without b's storage class, which isn't the default, b's.
		resourceChange("google_storage_bucket.b", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"storage_class": "NEARLINE"}, map[string]any{"storage_class": nil}),
	}}
	tf := &fakeTerraform{dir: dir, schemas: schemas, showns: []*tfjson.Plan{first, importedPlan("google_storage_bucket.a", "google_storage_bucket.b")}}
	defaults := map[string][]string{"google_storage_bucket": {"storage_class=STANDARD", "rpo=DEFAULT", "soft_delete_policy.retention_duration_seconds=604800"}}

	stripped, _, err := settleDefaults(tf, dir, changes, defaultRules{defaults: defaults})
	if err != nil || !stripped {
		t.Fatalf("want defaults stripped, got %v, %v", stripped, err)
	}
	got := squashed(readFile(t, dir, rootfile.GeneratedFileName))
	for _, want := range []string{
		`resource "google_storage_bucket" "a" { name = "a" rpo = "DEFAULT" location = "EU" }`,
		`storage_class = "NEARLINE" soft_delete_policy { retention_duration_seconds = 0 }`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, got)
		}
	}
}

// A difference in a nested block keeps what is on its path, not the rest
// of the top-level block it is in; a difference no candidate is on keeps
// all of that top-level block's, and one under no candidate's top-level
// argument all of the resource's.
func TestKeepChangedFollowsNestedPaths(t *testing.T) {
	t.Parallel()
	candidate := func(path ...string) defaultCandidate {
		return defaultCandidate{address: "aws_cloudfront_distribution.a", path: path}
	}
	removed := []defaultCandidate{
		candidate("default_cache_behavior", "compress"),
		candidate("default_cache_behavior", "min_ttl"),
		candidate("default_cache_behavior", "grpc_config"),
		candidate("origin", "connection_attempts"),
		candidate("retain_on_delete"),
	}
	kept := func(changed, paths []string) []string {
		keep := map[string]bool{}
		keepChanged(removed, "aws_cloudfront_distribution.a", changed, paths, keep)
		var got []string
		for _, c := range removed {
			if keep[c.key()] {
				got = append(got, strings.Join(c.path, "."))
			}
		}
		return got
	}
	for _, tc := range []struct {
		name          string
		changed, path []string
		want          string
	}{
		{"nested block", []string{"default_cache_behavior"}, []string{"default_cache_behavior.grpc_config.enabled"}, "default_cache_behavior.grpc_config"},
		{"nested argument", []string{"default_cache_behavior"}, []string{"default_cache_behavior.min_ttl"}, "default_cache_behavior.min_ttl"},
		{"whole block", []string{"default_cache_behavior"}, []string{"default_cache_behavior"}, "default_cache_behavior.compress,default_cache_behavior.min_ttl,default_cache_behavior.grpc_config"},
		{"no candidate on the path", []string{"default_cache_behavior"}, []string{"default_cache_behavior.target_origin_id"}, "default_cache_behavior.compress,default_cache_behavior.min_ttl,default_cache_behavior.grpc_config"},
		{"no candidate under it", []string{"enabled"}, []string{"enabled"}, "default_cache_behavior.compress,default_cache_behavior.min_ttl,default_cache_behavior.grpc_config,origin.connection_attempts,retain_on_delete"},
	} {
		if got := strings.Join(kept(tc.changed, tc.path), ","); got != tc.want {
			t.Errorf("%s: kept %s, want %s", tc.name, got, tc.want)
		}
	}
}
