// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

// An import Terraform generated no configuration for, such as of a KMS key
// scheduled for deletion, is found.
func TestUnconfigured(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, "resource \"aws_kms_key\" \"live\" {\n}\n")
	content, err := ImportsFile([]Import{{Type: "aws_kms_key", Name: "live", ID: "k-1"}, {Type: "aws_kms_key", Name: "pending", ID: "k-2"}})
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, ImportsFileName, string(content))

	missing, err := unconfigured(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for addr := range missing {
		got = append(got, addr)
	}
	slices.Sort(got)
	if want := []string{"aws_kms_key.pending"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A resource left out with secrets says which variables it reads:
// variables.tf declares them no longer.
func TestRejectNamesTheVariablesTheResourceReads(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "aws_ssm_parameter" "p" {
  name  = "p"
  value = var.aws_ssm_parameter_p_value
  tags  = { k = "${var.aws_ssm_parameter_p_tag}-x", other = local.var.x, again = var.aws_ssm_parameter_p_value }
}
`)
	content, err := ImportsFile([]Import{{Type: "aws_ssm_parameter", Name: "p", ID: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, ImportsFileName, string(content))

	var out bytes.Buffer
	if _, err := reject(dir, map[string][]tfjson.Diagnostic{"aws_ssm_parameter.p": {{Severity: tfjson.DiagnosticSeverityError, Summary: "Bad"}}}, &out); err != nil {
		t.Fatal(err)
	}

	want := "# Before moving it back, declare the sensitive variables it reads in variables.tf: aws_ssm_parameter_p_value, aws_ssm_parameter_p_tag.\n"
	if !strings.Contains(out.String(), want) {
		t.Errorf("missing %q in:\n%s", want, out.String())
	}
}
