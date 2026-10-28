// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package hclfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func TestParseHCLErrorsNameTheFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(path, []byte("resource \"a\" \"b\" {\n  x = \n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Each diagnostic names the file and the position too, as Terraform
	// does.
	if _, err := Parse("main.tf", []byte("x = \n")); err == nil || !strings.HasPrefix(err.Error(), "parse main.tf: main.tf:1,") {
		t.Errorf("parseHCL: got %v", err)
	}
	if _, err := Load(path); err == nil || !strings.HasPrefix(err.Error(), "parse "+path+": "+path+":2,") {
		t.Errorf("loadHCL: got %v", err)
	}
	if _, err := ReadVarFile(path); err == nil || !strings.HasPrefix(err.Error(), path+": "+path+":2,") {
		t.Errorf("readVarFile: got %v", err)
	}
}

func TestReadVarFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if f, err := ReadVarFile(filepath.Join(dir, "terraform.tfvars")); f != nil || err != nil {
		t.Errorf("no file: got %v, %v", f, err)
	}
	path := filepath.Join(dir, "terraform.tfvars")
	if err := os.WriteFile(path, []byte("region = \"eu-west-2\"\ncount = 2\nname = var.other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := ReadVarFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Only literals have a value.
	got := f.Values()
	if len(got) != 2 || !got["region"].RawEquals(cty.StringVal("eu-west-2")) || !got["count"].RawEquals(cty.NumberIntVal(2)) {
		t.Errorf("got %#v", got)
	}
}

func TestParseExprRoundTrips(t *testing.T) {
	t.Parallel()
	for _, src := range []string{
		`"a"`,
		`var.region`,
		`merge(local.tags, { Name = "x" })`,
		`[for s in var.subnets : s.id if s.public]`,
		`var.env == "prod" ? 3 : 1`,
		"{\n  a = 1\n  b = [2, 3]\n}",
	} {
		// The tokens have the space the argument puts before its value.
		if got := string(ParseExpr(src).Bytes()); got != " "+src {
			t.Errorf("got %q, want %q", got, src)
		}
	}
	for _, src := range []string{`"a`, `a = 1`, ``} {
		if tokens := ParseExpr(src); tokens != nil {
			t.Errorf("%q: got %q, want nil", src, tokens.Bytes())
		}
	}
	if got, want := string(FormatExpr([]byte("{\na=1\nlong_name=2\n}")).Bytes()), " {\n  a         = 1\n  long_name = 2\n}"; got != want {
		t.Errorf("formatExpr: got %q, want %q", got, want)
	}
}

func TestHCLFileSavesEdits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := writeConfig(t, dir, "generated.tf", `resource "aws_vpc" "a" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_vpc" "b" {
  cidr_block = "10.1.0.0/16"
}
`)
	resources := f.Resources()
	if len(resources) != 2 {
		t.Fatalf("got %d resources", len(resources))
	}
	f.Edit().Body().RemoveBlock(resources[0].Write)
	resources[1].Write.Body().SetAttributeValue("enable_dns_hostnames", cty.True)
	// The syntax tree describes the file as loaded.
	if len(f.Syntax.Blocks) != 2 {
		t.Errorf("syntax: got %d blocks, want 2", len(f.Syntax.Blocks))
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	want := `resource "aws_vpc" "b" {
  cidr_block           = "10.1.0.0/16"
  enable_dns_hostnames = true
}
`
	if got := readFile(t, dir, "generated.tf"); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	saved := New(filepath.Join(dir, "locals.tf"))
	saved.Edit().Body().AppendNewBlock("locals", nil).Body().SetAttributeValue("env", cty.StringVal("prod"))
	if err := saved.Save(); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, dir, "locals.tf"), "locals {\n  env = \"prod\"\n}\n"; got != want {
		t.Errorf("new file: got %q, want %q", got, want)
	}
}

// writeConfig writes content to the file name in dir and loads it.
func writeConfig(t *testing.T, dir, name, content string) *File {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
