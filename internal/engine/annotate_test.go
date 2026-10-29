// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// annotated makes edit of src, as generated.tf, in memory, and returns the
// file as the annotation stage keeps it, and whether edit changed it.
func annotated(t *testing.T, src string, edit func(f *hclfile.File) bool) (string, bool) {
	t.Helper()
	f, err := hclfile.Parse(rootfile.GeneratedFileName, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if !edit(f) {
		return src, false
	}
	return string(bytes.TrimLeft(hclwrite.Format(f.Edit().Bytes()), "\n")), true
}

// editFile makes edit of the file at path, and saves it if edit changed
// it, for a test that reads the file from disk.
func editFile(path string, edit func(f *hclfile.File) bool) (bool, error) {
	f, err := hclfile.Load(path)
	if err != nil || !edit(f) {
		return false, err
	}
	return true, f.Save()
}

// generatedText is generated.tf as ws has it.
func generatedText(t *testing.T, ws *workspace) string {
	t.Helper()
	f, err := ws.Load(rootfile.GeneratedFileName)
	if err != nil {
		t.Fatal(err)
	}
	return string(f.Src)
}

// The stage makes each annotation on the file as the ones before left it,
// and plans again for those that changed it and say they need it: not for
// one that changed nothing, nor for one that needs no plan.
func TestAnnotateDecidesTheReplan(t *testing.T) {
	t.Parallel()
	ws := memoryWorkspace(map[string]string{rootfile.GeneratedFileName: "resource \"aws_sqs_queue\" \"a\" {\n  name = \"a\"\n}\n"})
	var seen []string
	setting := func(name, value string) func(f *hclfile.File) (bool, error) {
		return func(f *hclfile.File) (bool, error) {
			seen = append(seen, string(f.Src))
			f.Edit().Body().Blocks()[0].Body().SetAttributeRaw(name, hclfile.ParseExpr(value))
			return true, nil
		}
	}
	unchanged := func(*hclfile.File) (bool, error) { return false, nil }

	replan, err := annotate(ws, []annotation{
		{name: "delay set", replan: true, edit: setting("delay_seconds", "5")},
		{name: "nothing", replan: true, edit: unchanged},
		{name: "comment", edit: setting("visibility_timeout_seconds", "30")},
		{name: "retention set", replan: true, edit: setting("message_retention_seconds", "60")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"delay set", "retention set"}; !reflect.DeepEqual(replan, want) {
		t.Errorf("replan: got %v, want %v", replan, want)
	}
	if len(seen) != 3 || !strings.Contains(seen[1], "delay_seconds") || !strings.Contains(seen[2], "visibility_timeout_seconds") {
		t.Errorf("each annotation should see the ones before it: %q", seen)
	}
	got := generatedText(t, ws)
	for _, want := range []string{"delay_seconds = 5", "visibility_timeout_seconds = 30", "message_retention_seconds = 60"} {
		if !strings.Contains(squashed(got), want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	if replan, err := annotate(ws, []annotation{{name: "nothing", replan: true, edit: unchanged}}); err != nil || replan != nil {
		t.Errorf("unchanged: got %v, %v, want no replan", replan, err)
	}
}

// An annotation's error stops the stage.
func TestAnnotateStopsOnError(t *testing.T) {
	t.Parallel()
	ws := memoryWorkspace(map[string]string{rootfile.GeneratedFileName: "resource \"aws_sqs_queue\" \"a\" {}\n"})
	failed := errors.New("failed")
	ran := false
	_, err := annotate(ws, []annotation{
		{name: "fails", replan: true, edit: func(*hclfile.File) (bool, error) { return false, failed }},
		{name: "after", replan: true, edit: func(*hclfile.File) (bool, error) { ran = true; return true, nil }},
	})
	if !errors.Is(err, failed) || ran {
		t.Errorf("got %v, ran after=%v, want %v and nothing after", err, ran, failed)
	}
}

// The annotations before Generate reads generated.tf say why it plans
// again, and a file they leave as it is needs no plan.
func TestGeneratedAnnotationsNameWhyItReplans(t *testing.T) {
	t.Parallel()
	ws := memoryWorkspace(map[string]string{rootfile.GeneratedFileName: `resource "aws_sqs_queue" "b" {
  name   = "b"
  policy = "{}"
}

resource "aws_sqs_queue" "a" {
  name = "a"
}
`})
	annotations := generatedAnnotations(nil, Options{Omit: map[string][]string{"aws_sqs_queue": {"policy"}}}, schemaHints{})

	replan, err := annotate(ws, annotations)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"resources sorted", "arguments left out"}; !reflect.DeepEqual(replan, want) {
		t.Errorf("replan: got %v, want %v", replan, want)
	}
	if got, want := generatedText(t, ws), `resource "aws_sqs_queue" "a" {
  name = "a"
}

resource "aws_sqs_queue" "b" {
  name = "b"
}
`; got != want {
		t.Errorf("generated.tf: got\n%s\nwant\n%s", got, want)
	}

	if replan, err := annotate(ws, annotations); err != nil || len(replan) != 0 {
		t.Errorf("again: got %v, %v, want no replan", replan, err)
	}
}

// Each annotation of generated.tf that changes it is a reason for the plan
// that follows: placeholders, lifecycles and the conflicting arguments it
// settles.
func TestGeneratedAnnotationsReplan(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		config string
		opts   Options
		hints  schemaHints
		want   []string
	}{
		"placeholders": {
			config: `resource "aws_lambda_function" "zip" {
  filename      = null
  function_name = "orders"
}
`,
			opts: Options{Placeholders: lambdaPlaceholder},
			want: []string{"placeholders set"},
		},
		"lifecycles": {
			config: `resource "aws_api_gateway_deployment" "a" {
  rest_api_id = "a1b2c3"
}
`,
			opts: Options{Lifecycles: map[string]Lifecycle{"aws_api_gateway_deployment": {CreateBeforeDestroy: true, Reason: "a stage refers to it"}}},
			want: []string{"lifecycles set"},
		},
		"conflicts": {
			config: `resource "aws_subnet" "a" {
  availability_zone    = "us-east-1a"
  availability_zone_id = "us-east-1-az1"
}
`,
			hints: exactHints(subnetMetadata()),
			want:  []string{"conflicting arguments settled"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ws := memoryWorkspace(map[string]string{rootfile.GeneratedFileName: tc.config})

			replan, err := annotate(ws, generatedAnnotations(nil, tc.opts, tc.hints))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(replan, tc.want) {
				t.Errorf("replan: got %v, want %v", replan, tc.want)
			}
		})
	}
}

// Noting principals, once the references are made, needs no plan: a
// comment changes neither the plan nor the lines its errors point at.
func TestReferencedAnnotationsNeedNoReplan(t *testing.T) {
	t.Parallel()
	ws := memoryWorkspace(map[string]string{rootfile.GeneratedFileName: `resource "azurerm_role_assignment" "group" {
  principal_id = "aaaaaaaa-0000-0000-0000-000000000003"
}
`})
	replan, err := annotate(ws, referencedAnnotations())
	if err != nil || len(replan) != 0 {
		t.Fatalf("got %v, %v, want no replan", replan, err)
	}
	if got := generatedText(t, ws); !strings.Contains(got, "# Entra ID object") {
		t.Errorf("not noted:\n%s", got)
	}
}
