// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package hclfile

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

// OptionalTypeTokens is the type of a generated module's variable, with
// every attribute of its objects optional, such as a route table's routes:
// a call then sets only the attributes it has values for (see
// WithoutNullAttributes), instead of a dozen nulls per object.
func OptionalTypeTokens(ty cty.Type) (hclwrite.Tokens, error) {
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

// HasObjects reports whether ty is or holds an object type, whose
// attributes OptionalTypeTokens makes optional.
func HasObjects(ty cty.Type) bool {
	switch {
	case ty.IsObjectType():
		return true
	case ty.IsListType(), ty.IsSetType(), ty.IsMapType():
		return HasObjects(ty.ElementType())
	case ty.IsTupleType():
		return slices.ContainsFunc(ty.TupleElementTypes(), HasObjects)
	}
	return false
}
