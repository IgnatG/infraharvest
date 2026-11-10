// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
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

// planOutput is the machine-readable UI output of a plan of n resources:
// a planned change and an apply-style progress line each, as large as a
// resource's, a warning, and the change summary.
func planOutput(n int) []byte {
	var b bytes.Buffer
	b.WriteString(`{"@level":"info","@message":"Terraform 1.17.0","type":"version","terraform":"1.17.0","ui":"1.2"}` + "\n")
	attrs := strings.Repeat(`\"k\":\"some value with \\\"diagnostic\\\" in it\",`, 40)
	for i := range n {
		fmt.Fprintf(&b, `{"@level":"info","@message":"aws_sqs_queue.q%d: Plan to import","type":"planned_change","change":{"resource":{"addr":"aws_sqs_queue.q%d","resource_type":"aws_sqs_queue","resource_name":"q%d"},"action":"import","importing":{"id":"https://sqs/%d"},"detail":"%s"}}`+"\n", i, i, i, i, attrs)
		fmt.Fprintf(&b, `{"@level":"info","@message":"aws_sqs_queue.q%d: Preparing import... [id=https://sqs/%d]","type":"import_start","hook":{"resource":{"addr":"aws_sqs_queue.q%d"}}}`+"\n", i, i, i)
	}
	b.WriteString(`{"@level":"info","@message":"named \"diagnostic\" and \"change_summary\"","type":"planned_change","change":{"detail":"diagnostic","x":"change_summary"}}` + "\n")
	b.WriteString(`{"@level":"warn","@message":"Warning: Deprecated","type":"diagnostic","diagnostic":{"severity":"warning","summary":"Deprecated"}}` + "\n")
	fmt.Fprintf(&b, `{"@level":"info","@message":"Plan: %d to import, 0 to add, 0 to change, 0 to destroy.","type":"change_summary","changes":{"add":0,"change":0,"import":%d,"remove":0,"operation":"plan"}}`+"\n", n, n)
	return b.Bytes()
}

// parsePlanOutput reads what parseUIDiagnostics and parseChangeSummary
// read, also where a line's other values name the types it looks for.
func TestParsePlanOutput(t *testing.T) {
	t.Parallel()
	out := planOutput(3)
	diags, summary, err := parsePlanOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := []tfjson.Diagnostic{{Severity: tfjson.DiagnosticSeverityWarning, Summary: "Deprecated"}}; !reflect.DeepEqual(diags, want) {
		t.Errorf("diagnostics: got %+v, want %+v", diags, want)
	}
	if want := (&ChangeSummary{Import: 3}); !reflect.DeepEqual(summary, want) {
		t.Errorf("summary: got %+v, want %+v", summary, want)
	}
	// Spaced as another encoder might write it.
	spaced := []byte(`{"type": "diagnostic", "diagnostic": {"severity": "error", "summary": "Bad"}}` + "\n" + `{"type" : "change_summary", "changes": {"add": 1}}` + "\n")
	diags, summary, err = parsePlanOutput(spaced)
	if err != nil || len(diags) != 1 || diags[0].Summary != "Bad" || summary == nil || summary.Add != 1 {
		t.Errorf("spaced output: got %+v, %+v, %v", diags, summary, err)
	}
	if _, _, err := parsePlanOutput(append(bytes.Repeat([]byte("x"), 17*1024*1024), '\n')); err == nil {
		t.Error("a line too long to read: want an error")
	}
}

// BenchmarkParsePlanOutput reads the diagnostics and the change summary
// of a plan of 20,000 resources.
func BenchmarkParsePlanOutput(b *testing.B) {
	out := planOutput(20000)
	b.SetBytes(int64(len(out)))
	for b.Loop() {
		if _, _, err := parsePlanOutput(out); err != nil {
			b.Fatal(err)
		}
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
