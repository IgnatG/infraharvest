// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"maps"
	"math"
	"reflect"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// A change whose source takes a hand edit gets no edit, and a reason that
// says why: a value that can't be written, a variable the root doesn't
// declare, an environment with no var file of its own, and a variable
// that sets more than what changed.
func TestDriftSourceTakesAHandEdit(t *testing.T) {
	t.Parallel()
	inProd := func(e *driftEditor) { e.env, e.envVars = "prod", hclfile.EnvVarsPath("prod") }
	for _, tc := range []struct {
		name   string
		files  map[string]string
		after  map[string]any
		change *Change
		setup  func(e *driftEditor)
		kind   string
		reason string
	}{
		{
			name: "a value that can't be written",
			files: map[string]string{"main.tf": `resource "aws_sqs_queue" "jobs" {
  delay_seconds = 30
}
`},
			after:  map[string]any{"delay_seconds": math.Inf(1)},
			change: driftAt("aws_sqs_queue.jobs", "delay_seconds", math.Inf(1)),
			kind:   sourceLiteral,
			reason: "the cloud's value can't be written: edit it by hand",
		},
		{
			name: "a value a shared local sets that can't be written",
			files: map[string]string{
				"main.tf": `resource "aws_sqs_queue" "jobs" {
  delay_seconds = local.delay
}

resource "aws_sqs_queue" "mail" {
  delay_seconds = local.delay
}
`,
				"locals.tf": `locals {
  delay = 30
}
`,
			},
			after:  map[string]any{"delay_seconds": math.Inf(1)},
			change: driftAt("aws_sqs_queue.jobs", "delay_seconds", math.Inf(1)),
			kind:   sourceSharedLocal,
			reason: "the cloud's value can't be written: edit it by hand",
		},
		{
			name: "a variable the root doesn't declare",
			files: map[string]string{"main.tf": `resource "aws_sqs_queue" "jobs" {
  delay_seconds = var.delay
}
`},
			after:  map[string]any{"delay_seconds": float64(0)},
			change: driftAt("aws_sqs_queue.jobs", "delay_seconds", float64(0)),
			kind:   sourceVariable,
			reason: "var.delay sets it, which the root doesn't declare: edit it by hand",
		},
		{
			name: "an environment with no var file of its own",
			files: map[string]string{"main.tf": `variable "delay" {}

resource "aws_sqs_queue" "jobs" {
  delay_seconds = var.delay
}
`},
			after:  map[string]any{"delay_seconds": float64(0)},
			change: driftAt("aws_sqs_queue.jobs", "delay_seconds", float64(0)),
			setup:  inProd,
			kind:   sourceVariable,
			reason: "var.delay sets it, and the root has no " + hclfile.EnvVarsPath("prod"),
		},
		{
			name: "a variable its var file sets to more than a literal",
			files: map[string]string{
				"main.tf": `variable "tags" {}

resource "aws_sqs_queue" "jobs" {
  tags = var.tags
}
`,
				hclfile.EnvVarsPath("prod"): `tags = merge({ Env = "prod" }, { Team = "core" })
`,
			},
			after:  map[string]any{"tags": map[string]any{"Env": "dev", "Team": "core"}},
			change: driftAt("aws_sqs_queue.jobs", "tags.Env", "dev"),
			setup:  inProd,
			kind:   sourceVariable,
			reason: "var.tags sets more than the cloud's value: change it in " + hclfile.EnvVarsPath("prod") + " by hand",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := testEditor(map[string]map[string]any{tc.change.Address: tc.after})
			if tc.setup != nil {
				tc.setup(e)
			}
			contents := byteFiles(tc.files)
			before := maps.Clone(contents)
			names, applied, failed, err := e.apply(contents, []*Change{tc.change})
			if err != nil {
				t.Fatal(err)
			}
			src, ok := failed[tc.change]
			if !ok {
				t.Fatalf("edited %v as %+v, want no edit", names, applied[tc.change])
			}
			if src.Kind != tc.kind || !strings.Contains(src.Reason, tc.reason) {
				t.Errorf("source %s: %q, want %s saying %q", src.Kind, src.Reason, tc.kind, tc.reason)
			}
			if !reflect.DeepEqual(contents, before) {
				t.Errorf("the files changed: %v", names)
			}
		})
	}
}

// hasSensitive finds a sensitive mark at any depth of a plan's
// after_sensitive mask, and only a true one.
func TestHasSensitive(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		mask any
		want bool
	}{
		{"nothing", nil, false},
		{"false", false, false},
		{"true", true, true},
		{"an empty object", map[string]any{}, false},
		{"an object of false", map[string]any{"a": false, "b": map[string]any{"c": false}}, false},
		{"a nested object", map[string]any{"a": false, "b": map[string]any{"c": true}}, true},
		{"an empty list", []any{}, false},
		{"a list of false", []any{false, false}, false},
		{"a list item", []any{false, true}, true},
		{"an object in a list", []any{map[string]any{"password": true}}, true},
		{"a list in an object", map[string]any{"rules": []any{false, []any{true}}}, true},
		{"another type", "true", false},
	} {
		if got := hasSensitive(tc.mask); got != tc.want {
			t.Errorf("%s: hasSensitive(%#v) = %v, want %v", tc.name, tc.mask, got, tc.want)
		}
	}
}

// diagError is an error diagnostic of a plan at line of file.
func diagError(file string, line int, summary string) tfjson.Diagnostic {
	pos := tfjson.Pos{Line: line, Column: 3}
	return tfjson.Diagnostic{Severity: tfjson.DiagnosticSeverityError, Summary: summary, Range: &tfjson.Range{Filename: file, Start: pos, End: pos}}
}

// A failed plan rejects the changes its errors are about, all of them
// for an error about none, and all of them where it reported neither
// errors nor a change summary. Each rejection keeps the first reason, with
// the secrets redacted.
func TestJudgeDriftWhenThePlanFails(t *testing.T) {
	t.Parallel()
	contents := byteFiles(map[string]string{
		"main.tf": `resource "aws_sqs_queue" "jobs" {
  delay_seconds = 0
}

resource "aws_sqs_queue" "mail" {
  delay_seconds = 5
}
`,
		"locals.tf": `locals {
  delay = 1
}
`,
	})
	jobs := driftAt("aws_sqs_queue.jobs", "delay_seconds", float64(0))
	mail := driftAt("aws_sqs_queue.mail", "delay_seconds", float64(5))
	viaLocal := driftAt("aws_sqs_queue.mail", "visibility_timeout_seconds", float64(1))
	sources := map[*Change]codeSource{
		jobs:     {File: "main.tf", Kind: sourceLiteral, Edit: &hclfile.Edit{File: "main.tf"}},
		mail:     {File: "main.tf", Kind: sourceLiteral, Edit: &hclfile.Edit{File: "main.tf"}},
		viaLocal: {File: "locals.tf", Kind: sourceLocal, Edit: &hclfile.Edit{File: "locals.tf"}},
	}
	pending := []*Change{jobs, mail, viaLocal}
	reasons := func(got map[*Change]string) map[string]string {
		out := map[string]string{}
		for c, reason := range got {
			out[c.Address+" "+c.Attribute] = reason
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		diags []tfjson.Diagnostic
		forms []string
		want  map[string]string
	}{
		{
			name:  "an error in one block",
			diags: []tfjson.Diagnostic{diagError("main.tf", 6, "Invalid value")},
			want:  map[string]string{"aws_sqs_queue.mail delay_seconds": "the plan of the edited code fails: main.tf:6: Invalid value"},
		},
		{
			name:  "an error in the locals",
			diags: []tfjson.Diagnostic{diagError("locals.tf", 2, "Invalid local")},
			want:  map[string]string{"aws_sqs_queue.mail visibility_timeout_seconds": "the plan of the edited code fails: locals.tf:2: Invalid local"},
		},
		{
			name:  "an error about none of them",
			diags: []tfjson.Diagnostic{{Severity: tfjson.DiagnosticSeverityError, Summary: "No valid credential sources found"}},
			want: map[string]string{
				"aws_sqs_queue.jobs delay_seconds":              "the plan of the edited code fails: No valid credential sources found",
				"aws_sqs_queue.mail delay_seconds":              "the plan of the edited code fails: No valid credential sources found",
				"aws_sqs_queue.mail visibility_timeout_seconds": "the plan of the edited code fails: No valid credential sources found",
			},
		},
		{
			name: "the first error a change is blamed for",
			diags: []tfjson.Diagnostic{
				diagError("main.tf", 2, "First"),
				diagError("main.tf", 2, "Second"),
			},
			want: map[string]string{"aws_sqs_queue.jobs delay_seconds": "the plan of the edited code fails: main.tf:2: First"},
		},
		{
			name:  "a secret in the error",
			diags: []tfjson.Diagnostic{diagError("main.tf", 2, "Invalid value hunter2-secret")},
			forms: []string{"hunter2-secret"},
			want:  map[string]string{"aws_sqs_queue.jobs delay_seconds": "the plan of the edited code fails: main.tf:2: Invalid value (sensitive value)"},
		},
		{
			name: "no errors and no plan",
			want: map[string]string{
				"aws_sqs_queue.jobs delay_seconds":              "terraform plan reported no change summary",
				"aws_sqs_queue.mail delay_seconds":              "terraform plan reported no change summary",
				"aws_sqs_queue.mail visibility_timeout_seconds": "terraform plan reported no change summary",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := reasons(judgeDrift(judgeInput{diags: tc.diags, pending: pending, sources: sources, contents: contents, forms: tc.forms}))
			for key, want := range tc.want {
				if !strings.HasPrefix(got[key], want) {
					t.Errorf("%s: %q, want %q", key, got[key], want)
				}
			}
			for key := range got {
				if _, ok := tc.want[key]; !ok {
					t.Errorf("%s rejected: %q", key, got[key])
				}
			}
		})
	}
}

// referredTo returns the resources the blocks of the addresses refer to,
// and nothing for code that doesn't parse or a block it doesn't have.
func TestReferredTo(t *testing.T) {
	t.Parallel()
	contents := byteFiles(map[string]string{"main.tf": `resource "aws_subnet" "a" {
  vpc_id     = aws_vpc.main.id
  cidr_block = cidrsubnet(aws_vpc.main.cidr_block, 8, 1)
  tags       = local.tags
}

resource "aws_route_table_association" "a" {
  subnet_id      = aws_subnet.a.id
  route_table_id = aws_route_table.private[0].id
}
`})
	for _, tc := range []struct {
		name      string
		contents  map[string][]byte
		addresses []string
		want      map[string]bool
	}{
		{"one block", contents, []string{"aws_subnet.a"}, map[string]bool{"aws_vpc.main": true, "local.tags": true}},
		{"an instance", contents, []string{`aws_route_table_association.a["x"]`}, map[string]bool{"aws_subnet.a": true, "aws_route_table.private": true}},
		{"no such block", contents, []string{"aws_vpc.main"}, map[string]bool{}},
		{"code that doesn't parse", byteFiles(map[string]string{"main.tf": "resource {"}), []string{"aws_subnet.a"}, map[string]bool{}},
	} {
		if got := referredTo(tc.contents, tc.addresses); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
