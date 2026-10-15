// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

func TestOptionalTypeString(t *testing.T) {
	route := cty.Set(cty.Object(map[string]cty.Type{"cidr_block": cty.String, "nat_gateway_id": cty.String, "tags": cty.Map(cty.String)}))
	for _, tc := range []struct {
		ty   cty.Type
		want string
	}{
		{route, "set(object({ cidr_block = optional(string), nat_gateway_id = optional(string), tags = optional(map(string)) }))"},
		{cty.List(cty.Number), "list(number)"},
		{cty.DynamicPseudoType, "any"},
	} {
		if got := optionalTypeString(tc.ty); got != tc.want {
			t.Errorf("optionalTypeString(%#v) = %s, want %s", tc.ty, got, tc.want)
		}
		if _, err := optionalTypeTokens(tc.ty); err != nil {
			t.Errorf("%s: %v", tc.want, err)
		}
	}
	if !hasObjects(route) || hasObjects(cty.Map(cty.String)) {
		t.Error("hasObjects")
	}
}

// Null attributes of objects go, wherever the objects are and whatever
// separates their attributes; the other values stay.
func TestWithoutNullAttributes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want cty.Value
	}{
		{`[{
  cidr_block      = "0.0.0.0/0"
  gateway_id      = null
  nat_gateway_id  = "nat-1"
  vpc_endpoint_id = null
}]`, cty.TupleVal([]cty.Value{cty.ObjectVal(map[string]cty.Value{"cidr_block": cty.StringVal("0.0.0.0/0"), "nat_gateway_id": cty.StringVal("nat-1")})})},
		{`[{ a = null, b = 1 }, { b = 2, a = null }]`, cty.TupleVal([]cty.Value{
			cty.ObjectVal(map[string]cty.Value{"b": cty.NumberIntVal(1)}),
			cty.ObjectVal(map[string]cty.Value{"b": cty.NumberIntVal(2)}),
		})},
		{`{ k = { a = null } }`, cty.ObjectVal(map[string]cty.Value{"k": cty.EmptyObjectVal})},
		{`["x"]`, cty.TupleVal([]cty.Value{cty.StringVal("x")})},
	} {
		f, diags := hclwrite.ParseConfig([]byte("x = "+tc.in+"\n"), "", hcl.InitialPos)
		if diags.HasErrors() {
			t.Fatal(diags)
		}
		out := withoutNullAttributes(f.Body().GetAttribute("x").Expr().BuildTokens(nil)).Bytes()
		if strings.Contains(string(out), "null") {
			t.Errorf("%s: still has nulls: %s", tc.in, out)
		}
		expr, diags := hclsyntax.ParseExpression(out, "", hcl.InitialPos)
		if diags.HasErrors() {
			t.Fatalf("%s: %s doesn't parse: %v", tc.in, out, diags)
		}
		got, diags := expr.Value(nil)
		if diags.HasErrors() || !got.RawEquals(tc.want) {
			t.Errorf("%s: got %#v, want %#v", tc.in, got, tc.want)
		}
	}
}
