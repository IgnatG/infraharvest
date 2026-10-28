// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// An azapi_resource whose pinned API version Terraform rejects, as when
// azapi's schemas don't have it, is imported again by its fallback, the
// bare ID, in one more plan; what else the first plan generated stays, and
// the result reports what it was imported by. A resource without a
// fallback is left out as before.
func TestGenerateRetriesFallbacks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const spec = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Resources/templateSpecs/spec"
	pinned := spec + "?api-version=2099-01-01"
	tf := &fakeTerraform{
		dir: dir,
		generated: `resource "azapi_resource" "spec" {
  type = "Microsoft.Resources/templateSpecs@2099-01-01"
}

resource "aws_sqs_queue" "b" {
  name = "b"
}
`,
		plans: []fakePlan{{diags: []tfjson.Diagnostic{errorAt(2, "Invalid configuration", `can't specify both the argument "tags" and "tags" in the argument "body"`)}}},
	}
	plans := 0
	tf.onPlan = func() {
		if plans++; plans == 2 {
			// The retry generates the spec alone, with azapi's version.
			tf.generated = "resource \"azapi_resource\" \"spec\" {\n  type = \"Microsoft.Resources/templateSpecs@2022-02-01\"\n}\n"
		}
	}
	imports := []rootfile.Import{
		{Type: "azapi_resource", Name: "spec", ID: pinned, Fallback: spec},
		{Type: "aws_sqs_queue", Name: "b", ID: "b"},
	}

	result, err := Generate(context.Background(), tf, dir, imports, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rejected) != 0 {
		t.Errorf("rejected: %+v", result.Rejected)
	}
	if got := readFile(t, dir, rootfile.ImportsFileName); strings.Contains(got, "api-version") || !strings.Contains(got, spec) {
		t.Errorf("imports.tf doesn't import the spec by its bare ID:\n%s", got)
	}
	got := readFile(t, dir, rootfile.GeneratedFileName)
	if strings.Count(got, `resource "azapi_resource" "spec"`) != 1 || !strings.Contains(got, "@2022-02-01") || !strings.Contains(got, `resource "aws_sqs_queue" "b"`) {
		t.Errorf("generated.tf:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, generatedBeforeRetry)); err == nil {
		t.Errorf("%s left behind", generatedBeforeRetry)
	}
	found := false
	for _, imp := range result.Imported {
		if imp.Type == "azapi_resource" {
			found = imp.ID == spec && imp.Fallback == ""
		}
	}
	if !found {
		t.Errorf("imported: got %+v, want the spec by %s", result.Imported, spec)
	}
}
