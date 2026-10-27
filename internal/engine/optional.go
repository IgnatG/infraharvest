// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// optionalTypeTokens is the type of a generated module's variable, with
// every attribute of its objects optional, such as a route table's routes:
// a call then sets only the attributes it has values for (see
// withoutNullAttributes), instead of a dozen nulls per object.
func optionalTypeTokens(ty cty.Type) (hclwrite.Tokens, error) {
	f, diags := hclwrite.ParseConfig([]byte("type = "+optionalTypeString(ty)+"\n"), "type", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, diags
	}
	return f.Body().GetAttribute("type").Expr().BuildTokens(nil), nil
}

// optionalTypeString is typeexpr.TypeString, with optional() around the
// attributes of object types.
func optionalTypeString(ty cty.Type) string {
	switch {
	case ty.IsListType():
		return "list(" + optionalTypeString(ty.ElementType()) + ")"
	case ty.IsSetType():
		return "set(" + optionalTypeString(ty.ElementType()) + ")"
	case ty.IsMapType():
		return "map(" + optionalTypeString(ty.ElementType()) + ")"
	case ty.IsTupleType():
		elems := make([]string, 0, ty.Length())
		for _, ety := range ty.TupleElementTypes() {
			elems = append(elems, optionalTypeString(ety))
		}
		return "tuple([" + strings.Join(elems, ", ") + "])"
	case ty.IsObjectType():
		atys := ty.AttributeTypes()
		names := make([]string, 0, len(atys))
		for name := range atys {
			names = append(names, name)
		}
		sort.Strings(names)
		attrs := make([]string, 0, len(names))
		for _, name := range names {
			if !hclsyntax.ValidIdentifier(name) {
				// A type with such a name can't be written; any takes it.
				return "any"
			}
			attrs = append(attrs, name+" = optional("+optionalTypeString(atys[name])+")")
		}
		return "object({ " + strings.Join(attrs, ", ") + " })"
	}
	return typeexpr.TypeString(ty)
}

// withoutNullAttributes returns an expression without the attributes of
// its object constructors that are null, which a variable with optional
// attributes (see optionalTypeTokens) sets to null itself.
func withoutNullAttributes(tokens hclwrite.Tokens) hclwrite.Tokens {
	src := tokens.Bytes()
	expr, diags := hclsyntax.ParseExpression(src, "", hcl.InitialPos)
	if diags.HasErrors() {
		return tokens
	}
	type span struct{ start, end int }
	var nulls []span
	_ = hclsyntax.VisitAll(expr, func(n hclsyntax.Node) hcl.Diagnostics {
		obj, ok := n.(*hclsyntax.ObjectConsExpr)
		if !ok {
			return nil
		}
		for _, item := range obj.Items {
			if isNullLiteral(item.ValueExpr) {
				nulls = append(nulls, span{item.KeyExpr.Range().Start.Byte, item.ValueExpr.Range().End.Byte})
			}
		}
		return nil
	})
	if len(nulls) == 0 {
		return tokens
	}
	slices.SortFunc(nulls, func(a, b span) int { return a.start - b.start })
	var out []byte
	last := 0
	for _, s := range nulls {
		out = append(out, src[last:s.start]...)
		end := s.end
		// The item's separator: a comma, or the end of its line.
		for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
			end++
		}
		if end < len(src) && src[end] == ',' {
			end++
		}
		for end < len(src) && (src[end] == ' ' || src[end] == '\t' || src[end] == '\r' || src[end] == '\n') {
			end++
		}
		last = end
	}
	out = append(out, src[last:]...)
	if formatted := formatExpr(out); formatted != nil {
		return formatted
	}
	return tokens
}

// hasObjects reports whether ty is or holds an object type, whose
// attributes optionalTypeTokens makes optional.
func hasObjects(ty cty.Type) bool {
	switch {
	case ty.IsObjectType():
		return true
	case ty.IsListType(), ty.IsSetType(), ty.IsMapType():
		return hasObjects(ty.ElementType())
	case ty.IsTupleType():
		return slices.ContainsFunc(ty.TupleElementTypes(), hasObjects)
	}
	return false
}
