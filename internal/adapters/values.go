// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// literal returns the value of an expression without references, such as
// "eu" or [80, 443]: false for one that has references, such as
// local.region, or doesn't parse.
func literal(tokens hclwrite.Tokens) (cty.Value, bool) {
	if len(tokens) == 0 {
		return cty.NilVal, false
	}
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() || len(expr.Variables()) > 0 {
		return cty.NilVal, false
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() || !v.IsWhollyKnown() || v.IsNull() {
		return cty.NilVal, false
	}
	return v, true
}

// literalString returns a literal string's value.
func literalString(tokens hclwrite.Tokens) (string, bool) {
	v, ok := literal(tokens)
	if !ok || v.Type() != cty.String {
		return "", false
	}
	return v.AsString(), true
}

// literalBool returns a literal bool's value.
func literalBool(tokens hclwrite.Tokens) (value, ok bool) {
	v, ok := literal(tokens)
	if !ok || v.Type() != cty.Bool {
		return false, false
	}
	return v.True(), true
}

// emptyList reports whether an expression is an empty list literal.
func emptyList(tokens hclwrite.Tokens) bool {
	v, ok := literal(tokens)
	return ok && (v.Type().IsTupleType() || v.Type().IsListType() || v.Type().IsSetType()) && v.LengthInt() == 0
}

// mustAttr returns an argument's expression, or nil if it isn't set.
func mustAttr(r *Reader, name string) hclwrite.Tokens {
	tokens, _ := r.Attr(name)
	return tokens
}

// IgnoreDefault marks an argument read if it has the given value, the
// provider's default, such as a Terraform-only setting an import always
// records at its default; any other value stays unread, so that Done
// declines it.
func (r *Reader) IgnoreDefault(name string, want cty.Value) {
	attr := r.body.GetAttribute(name)
	if attr == nil {
		return
	}
	if v, ok := literal(attr.Expr().BuildTokens(nil)); ok && v.RawEquals(want) {
		r.read[name] = true
	}
}

// stringList returns values as a list of strings, empty if there are none.
func stringList(values []string) cty.Value {
	if len(values) == 0 {
		return cty.ListValEmpty(cty.String)
	}
	list := make([]cty.Value, 0, len(values))
	for _, v := range values {
		list = append(list, cty.StringVal(v))
	}
	return cty.ListVal(list)
}
