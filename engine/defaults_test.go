// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
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
	writeConfig(t, dir, GeneratedFileName, withDefaults)
	content, err := ImportsFile([]Import{{Type: "aws_thing", Name: "a", ID: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, ImportsFileName, string(content))
	return dir, map[string]string{"aws_thing.a": changeSignature(resourceChange("aws_thing.a", tfjson.Actions{tfjson.ActionNoop}, nil, nil))}
}

// Nulls and constant defaults go; a constant that isn't the default, as
// the plan shows, and computed arguments stay.
func TestStripDefaults(t *testing.T) {
	dir, changes := thingRoot(t)
	// Without retries = 0, the provider would use its own default.
	first := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{
		resourceChange("aws_thing.a", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"retries": 0}, map[string]any{"retries": 3}),
	}}
	tf := &fakeTerraform{dir: dir, schemas: thingSchemas, showns: []*tfjson.Plan{first, importedPlan("aws_thing.a")}}

	stripped, err := stripDefaults(context.Background(), tf, dir, changeSummary{}, changes, nil)
	if err != nil || !stripped {
		t.Fatalf("want defaults stripped, got %v, %v", stripped, err)
	}

	got := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{`name = "a"`, "computed = false", "retries = 0", "setting {"} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"description", "enabled", "tags", "mode", "limit"} {
		if strings.Contains(got, gone) {
			t.Errorf("generated.tf still has %s:\n%s", gone, got)
		}
	}
	if calls := strings.Join(tf.calls, ","); calls != "schema,plan,show,plan,show" {
		t.Errorf("calls: %s", calls)
	}
}

// An error the plan can't pin on a resource keeps every constant; nulls
// still go, as they are the same as leaving the argument out.
func TestStripDefaultsUndoesOnErrors(t *testing.T) {
	dir, changes := thingRoot(t)
	tf := &fakeTerraform{dir: dir, schemas: thingSchemas, plans: []fakePlan{{diags: []tfjson.Diagnostic{{Severity: tfjson.DiagnosticSeverityError, Summary: "Invalid provider configuration"}}}}}

	stripped, err := stripDefaults(context.Background(), tf, dir, changeSummary{}, changes, nil)
	if err != nil || !stripped {
		t.Fatalf("want the nulls stripped, got %v, %v", stripped, err)
	}
	got := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{"enabled = false", "retries = 0", "tags = {}", `mode = ""`} {
		if !strings.Contains(got, want) {
			t.Errorf("generated.tf misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "null") {
		t.Errorf("generated.tf still has nulls:\n%s", got)
	}
}

func TestNoWorse(t *testing.T) {
	for _, tc := range []struct {
		now, before string
		want        bool
	}{
		{"no-op:", "no-op:", true},
		{"no-op:", "update:force_destroy", true},
		{"update:force_destroy", "update:force_destroy,tags", true},
		{"update:retries", "no-op:", false},
		{"update:force_destroy,retries", "update:force_destroy", false},
		{"delete,create:", "no-op:", false},
	} {
		if got := noWorse(tc.now, tc.before); got != tc.want {
			t.Errorf("noWorse(%q, %q) = %v", tc.now, tc.before, got)
		}
	}
}
