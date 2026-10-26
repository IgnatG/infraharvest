// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package stackintent

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/pkg/cfn"
)

// update rewrites the golden files from what Apply does now:
//
//	go test ./internal/stackintent -run TestGolden -update
//
// or, without a local toolchain, the remote workflow's golden task.
var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// fixtureStack is testdata/<case>/stack.json: the stack as --cfn-stack
// reads it, and where its resources were imported.
type fixtureStack struct {
	Name       string              `json:"name"`
	ID         string              `json:"id"`
	Parameters map[string]string   `json:"parameters"`
	Resolved   map[string]string   `json:"resolved"`
	Addresses  map[string][]string `json:"addresses"`
}

// Each case in testdata is a template, a fake stack and a root as Generate
// wrote it: Apply's edits, its file and its warnings are the case's golden
// file, testdata/<case>.golden.tf.
func TestGolden(t *testing.T) {
	cases, err := filepath.Glob(filepath.Join("testdata", "*", "stack.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases in testdata")
	}
	for _, stackFile := range cases {
		dir := filepath.Dir(stackFile)
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			got := applyCase(t, dir)
			golden := filepath.Join("testdata", name+".golden.tf")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v: run go test ./internal/stackintent -run TestGolden -update", err)
			}
			if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), got) {
				t.Errorf("%s differs; got:\n%s", golden, got)
			}
		})
	}
}

// applyCase applies a case to a copy of its root and renders the result.
func applyCase(t *testing.T, dir string) []byte {
	t.Helper()
	var fx fixtureStack
	content, err := os.ReadFile(filepath.Join(dir, "stack.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &fx); err != nil {
		t.Fatal(err)
	}
	templates, _ := filepath.Glob(filepath.Join(dir, "template.*"))
	if len(templates) != 1 {
		t.Fatalf("%s needs one template.yaml or template.json", dir)
	}
	template, err := os.ReadFile(templates[0])
	if err != nil {
		t.Fatal(err)
	}
	rootDir := t.TempDir()
	inputs, _ := filepath.Glob(filepath.Join(dir, "root", "*.tf"))
	for _, in := range inputs {
		b, err := os.ReadFile(in)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rootDir, filepath.Base(in)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stack := Stack{Name: fx.Name, ID: fx.ID, Template: string(template), Parameters: fx.Parameters, Resolved: fx.Resolved, Addresses: fx.Addresses}
	warnings, err := Apply(rootDir, []Stack{stack}, cfn.DefaultMapper())
	if err != nil {
		t.Fatal(err)
	}
	outputs, _ := filepath.Glob(filepath.Join(rootDir, "*.tf"))
	var b bytes.Buffer
	for _, out := range outputs {
		src, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if _, diags := hclsyntax.ParseConfig(src, out, hcl.InitialPos); diags.HasErrors() {
			t.Errorf("%s doesn't parse: %v", filepath.Base(out), diags)
		}
		fmt.Fprintf(&b, "# ---- %s ----\n\n%s\n", filepath.Base(out), src)
	}
	b.WriteString("# ---- warnings ----\n")
	for _, w := range warnings {
		fmt.Fprintf(&b, "# %s\n", w)
	}
	// The same input gives the same output.
	if again := applyCaseOnce(t, dir, stack); again != b.String() {
		t.Errorf("a second Apply differs:\n%s", again)
	}
	return b.Bytes()
}

// applyCaseOnce applies a case again, rendered as applyCase does.
func applyCaseOnce(t *testing.T, dir string, stack Stack) string {
	t.Helper()
	rootDir := t.TempDir()
	inputs, _ := filepath.Glob(filepath.Join(dir, "root", "*.tf"))
	for _, in := range inputs {
		b, _ := os.ReadFile(in)
		_ = os.WriteFile(filepath.Join(rootDir, filepath.Base(in)), b, 0o644)
	}
	warnings, err := Apply(rootDir, []Stack{stack}, cfn.DefaultMapper())
	if err != nil {
		t.Fatal(err)
	}
	outputs, _ := filepath.Glob(filepath.Join(rootDir, "*.tf"))
	var b strings.Builder
	for _, out := range outputs {
		src, _ := os.ReadFile(out)
		fmt.Fprintf(&b, "# ---- %s ----\n\n%s\n", filepath.Base(out), src)
	}
	b.WriteString("# ---- warnings ----\n")
	for _, w := range warnings {
		fmt.Fprintf(&b, "# %s\n", w)
	}
	return b.String()
}

func TestParameterValue(t *testing.T) {
	tests := []struct {
		typ, live, hcl, literal string
		value                   cty.Value
		ok                      bool
	}{
		{"String", "a${b}", "string", `"a$${b}"`, cty.StringVal("a${b}"), true},
		{"AWS::EC2::VPC::Id", "vpc-1", "string", `"vpc-1"`, cty.StringVal("vpc-1"), true},
		{"Number", "30", "number", "30", cty.NumberIntVal(30), true},
		{"Number", "thirty", "", "", cty.NilVal, false},
		{"List<Number>", "1, 2", "list(number)", "[1, 2]", cty.ListVal([]cty.Value{cty.NumberIntVal(1), cty.NumberIntVal(2)}), true},
		{"CommaDelimitedList", "a, b", "list(string)", `["a", "b"]`, cty.ListVal([]cty.Value{cty.StringVal("a"), cty.StringVal("b")}), true},
		{"List<AWS::EC2::Subnet::Id>", "", "list(string)", "[]", cty.ListValEmpty(cty.String), true},
	}
	for _, tt := range tests {
		typ, literal, value, ok := parameterValue(tt.typ, tt.live)
		if ok != tt.ok || typ != tt.hcl || literal != tt.literal || (ok && !value.RawEquals(tt.value)) {
			t.Errorf("parameterValue(%s, %q) = %s, %s, %#v, %v", tt.typ, tt.live, typ, literal, value, ok)
		}
	}
}

func TestSSMValue(t *testing.T) {
	tests := []struct {
		resolved string
		list     bool
		want     cty.Value
	}{
		{"ami-1", false, cty.StringVal("ami-1")},
		{"a, b", false, cty.StringVal("a, b")},
		// split() doesn't trim, so neither does the value compared.
		{"subnet-1, subnet-2", true, cty.ListVal([]cty.Value{cty.StringVal("subnet-1"), cty.StringVal(" subnet-2")})},
		{"", true, cty.ListVal([]cty.Value{cty.StringVal("")})},
	}
	for _, tt := range tests {
		if got := ssmValue(tt.resolved, tt.list); !got.RawEquals(tt.want) {
			t.Errorf("ssmValue(%q, %v) = %#v", tt.resolved, tt.list, got)
		}
	}
}

func TestSameValue(t *testing.T) {
	tests := []struct {
		got, want cty.Value
		same      bool
	}{
		{cty.StringVal("a"), cty.StringVal("a"), true},
		{cty.StringVal("30"), cty.NumberIntVal(30), false},
		// A String parameter that sets a number or a bool: Terraform converts.
		{cty.NumberIntVal(30), cty.StringVal("30"), true},
		{cty.NumberIntVal(30), cty.StringVal("31"), false},
		{cty.NumberFloatVal(30.0), cty.NumberIntVal(30), true},
		{cty.TupleVal([]cty.Value{cty.StringVal("a")}), cty.ListVal([]cty.Value{cty.StringVal("a")}), true},
		{cty.TupleVal([]cty.Value{cty.StringVal("a")}), cty.ListVal([]cty.Value{cty.StringVal("b")}), false},
		{cty.True, cty.StringVal("true"), true},
		{cty.False, cty.StringVal("true"), false},
	}
	for _, tt := range tests {
		if got := sameValue(tt.got, tt.want); got != tt.same {
			t.Errorf("sameValue(%#v, %#v) = %v", tt.got, tt.want, got)
		}
	}
}

func TestSubVariables(t *testing.T) {
	got := strings.Join(subVariables("${A}-${!B}-${ C.D }-${"), ",")
	if got != "A,C.D" {
		t.Errorf("subVariables = %s", got)
	}
}
