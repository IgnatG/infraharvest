// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

func TestParseUIDiagnostics(t *testing.T) {
	out := `{"@level":"info","@message":"Terraform 1.16.5","type":"version"}
{"@level":"error","@message":"Error: Invalid combination","type":"diagnostic","diagnostic":{"severity":"error","summary":"Invalid combination","detail":"one of a,b","address":"aws_x.a","range":{"filename":"generated.tf","start":{"line":4,"column":3,"byte":40},"end":{"line":4,"column":8,"byte":45}}}}
not json
{"@level":"warn","type":"diagnostic","diagnostic":{"severity":"warning","summary":"Deprecated"}}
`
	got, err := parseUIDiagnostics([]byte(out))
	if err != nil {
		t.Fatal(err)
	}

	want := []tfjson.Diagnostic{
		{
			Severity: tfjson.DiagnosticSeverityError, Summary: "Invalid combination", Detail: "one of a,b", Address: "aws_x.a",
			Range: &tfjson.Range{Filename: "generated.tf", Start: tfjson.Pos{Line: 4, Column: 3, Byte: 40}, End: tfjson.Pos{Line: 4, Column: 8, Byte: 45}},
		},
		{Severity: tfjson.DiagnosticSeverityWarning, Summary: "Deprecated"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if errs := errorDiagnostics(got); len(errs) != 1 || errs[0].Summary != "Invalid combination" {
		t.Errorf("errorDiagnostics: got %+v", errs)
	}
}

func TestFormatDiagnostic(t *testing.T) {
	d := errorAt(7, "Invalid value", "expected one of\n  a, b")
	if got, want := formatDiagnostic(d), "generated.tf:7: Invalid value: expected one of a, b"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := formatDiagnostic(tfjson.Diagnostic{Summary: "Plugin crashed"}), "Plugin crashed"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func writeConfig(t *testing.T, dir, name, content string) *hclFile {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := loadHCL(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestByResource(t *testing.T) {
	dir := t.TempDir()
	imports, err := ImportsFile([]Import{
		{Type: "aws_sqs_queue", Name: "a", ID: "a"},
		{Type: "aws_sqs_queue", Name: "a2", ID: "a2"},
		{Type: "aws_vpc", Name: "b", ID: "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	importsFile := writeConfig(t, dir, ImportsFileName, string(imports))
	generated := writeConfig(t, dir, GeneratedFileName, `resource "aws_vpc" "b" {
  cidr_block = "10.0.0.0/16"
}
`)
	// imports.tf: aws_sqs_queue.a on lines 1-4, aws_sqs_queue.a2 on 6-9.
	diags := []tfjson.Diagnostic{
		{Summary: "by address", Address: `aws_sqs_queue.a2["x"]`},
		errorAt(2, "in generated.tf", ""),
		importErrorAt(3, "in imports.tf"),
		{Summary: "Cannot import", Detail: `While importing aws_sqs_queue.a2: not found.`},
		{Summary: "two resources", Detail: "aws_sqs_queue.a and aws_vpc.b"},
		{Summary: "provider", Detail: "no credentials"},
		{Summary: "unknown address", Address: "aws_s3_bucket.c"},
	}

	got, general := byResource(diags, generated, importsFile)

	want := map[string][]tfjson.Diagnostic{
		"aws_sqs_queue.a2": {diags[0], diags[3]},
		"aws_vpc.b":        {diags[1]},
		"aws_sqs_queue.a":  {diags[2]},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("assigned: got %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(general, diags[4:]) {
		t.Errorf("unassigned: got %+v, want %+v", general, diags[4:])
	}
}

func TestOnlyMentioned(t *testing.T) {
	addresses := map[string]bool{"aws_sqs_queue.a": true, "aws_sqs_queue.a-b": true}
	for text, want := range map[string]string{
		`importing "aws_sqs_queue.a-b".`:              "aws_sqs_queue.a-b",
		"aws_sqs_queue.a, then aws_sqs_queue.a again": "aws_sqs_queue.a",
		"aws_sqs_queue.a and aws_sqs_queue.a-b":       "",
		"registry.terraform.io/hashicorp/aws":         "",
	} {
		if got := onlyMentioned(text, addresses); got != want {
			t.Errorf("%q: got %q, want %q", text, got, want)
		}
	}
}

func TestReject(t *testing.T) {
	dir := t.TempDir()
	imports, err := ImportsFile([]Import{{Type: "aws_vpc", Name: "a", ID: "vpc-a"}, {Type: "aws_vpc", Name: "b", ID: "vpc-b"}})
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, ImportsFileName, string(imports))
	writeConfig(t, dir, GeneratedFileName, `resource "aws_vpc" "a" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_vpc" "b" {
  cidr_block = "10.1.0.0/16"
}
`)
	var rejected bytes.Buffer
	got, err := reject(dir, map[string][]tfjson.Diagnostic{"aws_vpc.a": {errorAt(2, "Bad CIDR", "")}}, &rejected)
	if err != nil {
		t.Fatal(err)
	}

	if want := []Rejection{{Address: "aws_vpc.a", ID: "vpc-a", Errors: []string{"Bad CIDR"}}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	wantRejected := `# aws_vpc.a was left out:
#   Bad CIDR

import {
  to = aws_vpc.a
  id = "vpc-a"
}

resource "aws_vpc" "a" {
  cidr_block = "10.0.0.0/16"
}
`
	if got := rejected.String(); got != wantRejected {
		t.Errorf("rejected: got:\n%s\nwant:\n%s", got, wantRejected)
	}
	if got, want := readFile(t, dir, ImportsFileName), "import {\n  to = aws_vpc.b\n  id = \"vpc-b\"\n}\n"; got != want {
		t.Errorf("imports.tf: got:\n%s\nwant:\n%s", got, want)
	}
	if got, want := readFile(t, dir, GeneratedFileName), "resource \"aws_vpc\" \"b\" {\n  cidr_block = \"10.1.0.0/16\"\n}\n"; got != want {
		t.Errorf("generated.tf: got:\n%s\nwant:\n%s", got, want)
	}
}
