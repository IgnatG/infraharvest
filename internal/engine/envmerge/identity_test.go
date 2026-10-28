// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// addLogGroup adds to the root r, written by writeRootDir, a log group
// called name, imported by its identity, or by its ID if byID.
func addLogGroup(t *testing.T, r MergeRoot, name string, byID bool) {
	t.Helper()
	group := "/shop/" + r.Tokens[0] + "/" + name
	importBlock := "\nimport {\n  to = aws_cloudwatch_log_group." + name + "\n  identity = {\n    account_id = \"" + r.Scope + "\"\n    name       = \"" + group + "\"\n    region     = \"eu-west-2\"\n  }\n}\n"
	if byID {
		importBlock = "\nimport {\n  to = aws_cloudwatch_log_group." + name + "\n  id = \"" + group + "\"\n}\n"
	}
	for file, content := range map[string]string{
		"logs.tf":                "\nresource \"aws_cloudwatch_log_group\" \"" + name + "\" {\n  name = \"" + group + "\"\n}\n",
		rootfile.ImportsFileName: importBlock,
	} {
		f, err := os.OpenFile(filepath.Join(r.Dir, file), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString(content)
		if err := errors.Join(err, f.Close()); err != nil {
			t.Fatal(err)
		}
	}
}

// Roots that import resources by identity merge as those that import
// them by ID: the merged root's import blocks take each environment's
// identities from its imports.tfvars, and each environment's plan
// imports them.
func TestMergeRootsByIdentity(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
	var overrides []string
	roots := mergeRoots(t, dir, nil, &overrides)
	addLogGroup(t, roots[0], "app", false)
	addLogGroup(t, roots[1], "app", false)
	addLogGroup(t, roots[1], "audit", false)
	result, err := MergeRoots(context.Background(), dir, roots, MergeOptions{Profile: mergeProfiles["aws"], Gate: testGate})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != "" || !result.Passed() {
		t.Fatalf("not merged: %s %+v", result.Reason, result.Environments)
	}
	imports := readFile(t, dir, rootfile.ImportsFileName)
	for _, want := range []string{
		"to       = aws_cloudwatch_log_group.app\n  identity = var.import_identities[\"aws_cloudwatch_log_group.app\"]",
		"identity = var.import_identities[\"aws_cloudwatch_log_group.audit[0]\"]",
		"id = var.import_ids[\"aws_sqs_queue.orders\"]",
	} {
		if !strings.Contains(imports, want) {
			t.Errorf("imports.tf misses %q:\n%s", want, imports)
		}
	}
	if variables := readFile(t, dir, hclfile.VariablesFileName); !strings.Contains(variables, "variable \"import_identities\" {") || !strings.Contains(variables, "map(map(string))") {
		t.Errorf("variables.tf doesn't declare import_identities:\n%s", variables)
	}
	prod := readFile(t, dir, "vars/prod/imports.tfvars")
	for _, want := range []string{"\"aws_cloudwatch_log_group.app\" = {\n    account_id = \"222222222222\"\n    name       = \"/shop/prd/app\"", "\"aws_cloudwatch_log_group.audit[0]\" = {", "\"aws_sqs_queue.orders\""} {
		if !strings.Contains(prod, want) {
			t.Errorf("prod's imports.tfvars misses %q:\n%s", want, prod)
		}
	}
	if dev := readFile(t, dir, "vars/dev/imports.tfvars"); strings.Contains(dev, "audit") || !strings.Contains(dev, "/shop/dev/app") {
		t.Errorf("dev's imports.tfvars:\n%s", dev)
	}
	const id = "account_id=222222222222,name=/shop/prd/app,region=eu-west-2"
	if got := result.Environments[1].Imports["aws_cloudwatch_log_group.app"]; got != id {
		t.Errorf("prod's import of the log group: %q", got)
	}
	ids, err := hclfile.MergedImportIDs(dir, "prod")
	if err != nil || ids["aws_cloudwatch_log_group.app"] != id || ids["aws_sqs_queue.orders"] == "" {
		t.Errorf("MergedImportIDs: %v %v", ids, err)
	}
}

// One import block can't import a resource by ID in one environment and
// by identity in another.
func TestMergeRootsByIDAndIdentity(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
	var overrides []string
	roots := mergeRoots(t, dir, nil, &overrides)
	addLogGroup(t, roots[0], "app", true)
	addLogGroup(t, roots[1], "app", false)
	result, err := MergeRoots(context.Background(), dir, roots, MergeOptions{Profile: mergeProfiles["aws"], Gate: testGate})
	if err != nil || !strings.Contains(result.Reason, "by ID and others by identity") {
		t.Errorf("want a reason not to merge, got %v %+v", err, result)
	}
}
