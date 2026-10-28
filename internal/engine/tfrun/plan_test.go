// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"reflect"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

func TestParseUIDiagnostics(t *testing.T) {
	t.Parallel()
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
	if errs := ErrorDiagnostics(got); len(errs) != 1 || errs[0].Summary != "Invalid combination" {
		t.Errorf("ErrorDiagnostics: got %+v", errs)
	}
}

func TestFormatDiagnostic(t *testing.T) {
	t.Parallel()
	d := errorAt(7, "Invalid value", "expected one of\n  a, b")
	if got, want := FormatDiagnostic(d), "generated.tf:7: Invalid value: expected one of a, b"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := FormatDiagnostic(tfjson.Diagnostic{Summary: "Plugin crashed"}), "Plugin crashed"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// errorAt is an error diagnostic at line of generated.tf.
func errorAt(line int, summary, detail string) tfjson.Diagnostic {
	return tfjson.Diagnostic{
		Severity: tfjson.DiagnosticSeverityError,
		Summary:  summary,
		Detail:   detail,
		Range:    &tfjson.Range{Filename: "generated.tf", Start: tfjson.Pos{Line: line}, End: tfjson.Pos{Line: line}},
	}
}
