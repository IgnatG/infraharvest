// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"reflect"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/secret"
)

// copiedVariables are the variable blocks of src, as a rootWriter copies
// them from the roots.
func copiedVariables(t *testing.T, src string) []*hclwrite.Block {
	t.Helper()
	f, diags := hclwrite.ParseConfig([]byte(src), "variables.tf", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	return f.Body().Blocks()
}

// sensitiveIn returns the merged root's sensitive variables that the
// environment sets, and the copied variables the root declares sensitive
// and names as secrets, typed as they declare: no others.
func TestSensitiveIn(t *testing.T) {
	t.Parallel()
	w := &rootWriter{
		m: &stackMerge{variables: []*envVariable{
			{name: "db_password", ty: cty.String, sensitive: true, values: map[int]hclwrite.Tokens{0: nil}},
			{name: "api_token", ty: cty.String, sensitive: true, values: map[int]hclwrite.Tokens{1: nil}},
			{name: "queue_name", ty: cty.String, values: map[int]hclwrite.Tokens{0: nil}},
		}},
		noEnv: true,
		copied: copiedVariables(t, `variable "signing_key" {
  type      = map(string)
  sensitive = true
}

variable "webhook" {
  sensitive = true
}

variable "region" {
  type = string
}

variable "unnamed" {
  sensitive = true
}

variable "said_false" {
  sensitive = false
}

variable "not_a_literal" {
  sensitive = var.x
}

variable "bad_type" {
  type      = nonsense
  sensitive = true
}
`),
	}
	rootSecrets := []secret.Variable{{Variable: "signing_key"}, {Variable: "webhook"}, {Variable: "region"}, {Variable: "said_false"}, {Variable: "not_a_literal"}, {Variable: "bad_type"}}
	want := []secret.Variable{
		{Variable: "db_password", Type: cty.String},
		{Variable: "signing_key", Type: cty.Map(cty.String)},
		{Variable: "webhook", Type: cty.String},
		{Variable: "bad_type", Type: cty.String},
	}
	if got := w.sensitiveIn(0, rootSecrets); !reflect.DeepEqual(got, want) {
		t.Errorf("environment 0: %+v, want %+v", got, want)
	}
	want = []secret.Variable{
		{Variable: "api_token", Type: cty.String},
		{Variable: "signing_key", Type: cty.Map(cty.String)},
		{Variable: "webhook", Type: cty.String},
		{Variable: "bad_type", Type: cty.String},
	}
	if got := w.sensitiveIn(1, rootSecrets); !reflect.DeepEqual(got, want) {
		t.Errorf("environment 1: %+v, want %+v", got, want)
	}
}

// secretsIn returns an environment's secrets at the addresses the merged
// root has their resources at, and leaves out those of resources it
// doesn't have.
func TestSecretsIn(t *testing.T) {
	t.Parallel()
	shared := &stackResource{resourceType: "aws_db_instance", label: "main"}
	keyed := &stackResource{resourceType: "aws_ssm_parameter", label: "token", forEach: &forEachGroup{resourceType: "aws_ssm_parameter", label: "params"}}
	m := &stackMerge{index: []map[string]*envResource{
		{
			"aws_db_instance.dev":     {stack: shared},
			"aws_ssm_parameter.token": {stack: keyed},
			"aws_iam_user.unmatched":  {},
		},
		{"aws_db_instance.prod": {stack: shared}},
	}}
	g := &mergeGate{roots: []MergeRoot{
		{Secrets: []secret.Variable{
			{Variable: "db_password", Address: "aws_db_instance.dev", Attribute: "password"},
			{Variable: "token", Address: "aws_ssm_parameter.token", Attribute: "value"},
			{Variable: "unmatched", Address: "aws_iam_user.unmatched", Attribute: "x"},
			{Variable: "gone", Address: "aws_iam_user.gone", Attribute: "x"},
		}},
		{Secrets: []secret.Variable{{Variable: "db_password", Address: "aws_db_instance.prod", Attribute: "password"}}},
	}}
	want := []secret.Variable{
		{Variable: "db_password", Address: "aws_db_instance.main", Attribute: "password"},
		{Variable: "token", Address: `aws_ssm_parameter.params["token"]`, Attribute: "value"},
	}
	if got := g.secretsIn(m, 0); !reflect.DeepEqual(got, want) {
		t.Errorf("environment 0: %+v, want %+v", got, want)
	}
	want = []secret.Variable{{Variable: "db_password", Address: "aws_db_instance.main", Attribute: "password"}}
	if got := g.secretsIn(m, 1); !reflect.DeepEqual(got, want) {
		t.Errorf("environment 1: %+v, want %+v", got, want)
	}
}
