// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
)

const bucketGenerated = `resource "aws_s3_bucket" "artifacts" {
  bucket = "artifacts"
  policy = "{}"
  versioning {
    enabled = true
  }
  website {
  }
}

resource "aws_s3_bucket_versioning" "artifacts" {
  bucket = "artifacts"
  versioning_configuration {
    status = "Enabled"
  }
}
`

func TestGenerateOmitsArguments(t *testing.T) {
	dir := t.TempDir()
	tf := &fakeTerraform{dir: dir, generated: bucketGenerated}
	imports := []Import{{Type: "aws_s3_bucket", Name: "artifacts", ID: "artifacts"}, {Type: "aws_s3_bucket_versioning", Name: "artifacts", ID: "artifacts"}}

	_, err := Generate(context.Background(), tf, dir, imports, Options{Omit: map[string][]string{"aws_s3_bucket": {"policy", "versioning", "website", "logging"}}})
	if err != nil {
		t.Fatal(err)
	}

	got := readFile(t, dir, GeneratedFileName)
	for _, removed := range []string{"policy", "versioning {", "website"} {
		if strings.Contains(got, removed) {
			t.Errorf("%q not left out:\n%s", removed, got)
		}
	}
	// Only the bucket's arguments: the versioning resource keeps its own.
	for _, kept := range []string{`bucket = "artifacts"`, `resource "aws_s3_bucket_versioning" "artifacts"`, "versioning_configuration {"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q missing:\n%s", kept, got)
		}
	}
	// Leaving arguments out moves lines, so the errors are planned for again.
	if strings.Join(tf.calls, ",") != "init,plan,plan,plan,show,fmt,validate,plan,show" {
		t.Errorf("calls: got %v, want [init plan plan plan show fmt validate plan show]", tf.calls)
	}
}

func TestOmitArgumentsWithoutMatches(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, GeneratedFileName, bucketGenerated).path

	changed, err := OmitArguments(path, map[string][]string{"aws_sqs_queue": {"policy"}})

	if err != nil || changed {
		t.Errorf("want no change, got changed=%v err=%v", changed, err)
	}
}

const containersGenerated = `resource "azurerm_storage_container" "data" {
  default_encryption_scope          = "$account-encryption-key"
  encryption_scope_override_enabled = true
  name                              = "data"
}

resource "azurerm_storage_container" "logs" {
  default_encryption_scope          = "logs"
  encryption_scope_override_enabled = false
  name                              = "logs"
}
`

// omitScope leaves out the encryption scope Azure reports for a container
// that has none of its own, which azurerm rejects, and the override flag
// at its default, which needs a scope.
var omitScope = map[string][]string{"azurerm_storage_container": {"default_encryption_scope=$account-encryption-key", "encryption_scope_override_enabled=true"}}

// name=value leaves an attribute out only where it is set to value: the
// container with the account's default scope loses it, the one with a
// scope of its own keeps it.
func TestOmitArgumentsByValue(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, GeneratedFileName, containersGenerated).path

	changed, err := OmitArguments(path, omitScope)
	if err != nil || !changed {
		t.Fatalf("want a change, got changed=%v err=%v", changed, err)
	}

	f, err := loadHCL(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"data": {"name"},
		"logs": {"default_encryption_scope", "encryption_scope_override_enabled", "name"},
	}
	for _, b := range f.syntax.Blocks {
		var names []string
		for name := range b.Body.Attributes {
			names = append(names, name)
		}
		slices.Sort(names)
		if !slices.Equal(names, want[b.Labels[1]]) {
			t.Errorf("%s: got %v, want %v", b.Labels[1], names, want[b.Labels[1]])
		}
	}
}

// The standards check flags an argument set to the value it is left out
// for, and only that.
func TestCheckOmittedByValue(t *testing.T) {
	f := writeConfig(t, t.TempDir(), GeneratedFileName, containersGenerated)
	var findings []string
	for _, b := range f.syntax.Blocks {
		checkOmitted(b, omitScope, func(hcl.Range) string { return "x" }, func(rule, format string, args ...any) {
			findings = append(findings, rule+": "+fmt.Sprintf(format, args...))
		})
	}
	want := []string{
		"deprecated-argument: x: azurerm_storage_container.data sets default_encryption_scope",
		"deprecated-argument: x: azurerm_storage_container.data sets encryption_scope_override_enabled",
	}
	if !slices.Equal(findings, want) {
		t.Errorf("got %q, want %q", findings, want)
	}
}
