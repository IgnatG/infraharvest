// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"maps"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// attributeUnion returns the names of the arguments of bodies in, sorted.
func attributeUnion(in []envBody) []string {
	names := map[string]bool{}
	for _, x := range in {
		for name := range x.body.Attributes() {
			names[name] = true
		}
	}
	return slices.Sorted(maps.Keys(names))
}

// blockTypeUnion returns the types of the nested blocks of bodies in, in
// the order they first come.
func blockTypeUnion(in []envBody) []string {
	var types []string
	for _, x := range in {
		for _, blk := range x.body.Blocks() {
			if !slices.Contains(types, blk.Type()) {
				types = append(types, blk.Type())
			}
		}
	}
	return types
}

func blocksOf(body *hclwrite.Body, typ string) []*hclwrite.Block {
	var blocks []*hclwrite.Block
	for _, blk := range body.Blocks() {
		if blk.Type() == typ {
			blocks = append(blocks, blk)
		}
	}
	return blocks
}

// objectTokens is body's arguments as an object.
func objectTokens(body *hclwrite.Body) hclwrite.Tokens {
	var attrs []hclwrite.ObjectAttrTokens
	for _, name := range slices.Sorted(maps.Keys(body.Attributes())) {
		attrs = append(attrs, hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForIdentifier(name), Value: body.GetAttribute(name).Expr().BuildTokens(nil)})
	}
	return hclwrite.TokensForObject(attrs)
}

// exprKey is a key of tokens, an expression: the same for the same
// tokens, whatever the spaces between them.
func exprKey(tokens hclwrite.Tokens) string {
	var b strings.Builder
	for _, t := range tokens {
		if t.Type == hclsyntax.TokenComment {
			continue
		}
		b.Write(t.Bytes)
		b.WriteByte(0)
	}
	return b.String()
}

// sameExprs reports whether every one of values is set, and the same.
func sameExprs(values []hclwrite.Tokens) bool {
	for _, v := range values {
		if v == nil || exprKey(v) != exprKey(values[0]) {
			return false
		}
	}
	return true
}

// exprReads reports what tokens, an expression, refers to: resources,
// variables, and anything else, such as locals and data sources. An
// expression that doesn't parse reads something else.
func exprReads(tokens hclwrite.Tokens) (resources, variables, other bool) {
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() {
		return false, false, true
	}
	for _, t := range expr.Variables() {
		switch {
		case stackAddress(t) != "":
			resources = true
		case t.RootName() == "var":
			variables = true
		default:
			other = true
		}
	}
	return resources, variables, other
}

// literalTokens returns tokens, a value that reads nothing, as a var file
// or a variable's default can hold it: as it is without function calls,
// else evaluated, as jsonencode({...}) becomes the JSON string it gives;
// false where it can't be evaluated.
func literalTokens(tokens hclwrite.Tokens) (hclwrite.Tokens, bool) {
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, false
	}
	calls := false
	_ = hclsyntax.VisitAll(expr, func(n hclsyntax.Node) hcl.Diagnostics {
		if _, ok := n.(*hclsyntax.FunctionCallExpr); ok {
			calls = true
		}
		return nil
	})
	if !calls {
		return tokens, true
	}
	v, diags := expr.Value(hclfile.EvalContext)
	if diags.HasErrors() || !v.IsWhollyKnown() {
		return nil, false
	}
	return hclwrite.TokensForValue(v), true
}

// renameTokens returns tokens with each reference to a resource rename
// knows, by its address, made to what rename gives instead.
func renameTokens(tokens hclwrite.Tokens, rename func(string) (hclwrite.Tokens, bool)) hclwrite.Tokens {
	out := make(hclwrite.Tokens, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if t.Type == hclsyntax.TokenIdent && i+2 < len(tokens) && tokens[i+1].Type == hclsyntax.TokenDot && tokens[i+2].Type == hclsyntax.TokenIdent && (i == 0 || tokens[i-1].Type != hclsyntax.TokenDot) {
			if to, ok := rename(string(t.Bytes) + "." + string(tokens[i+2].Bytes)); ok && len(to) > 0 {
				for j, r := range to {
					c := *r
					if j == 0 {
						c.SpacesBefore = t.SpacesBefore
					}
					out = append(out, &c)
				}
				i += 2
				continue
			}
		}
		out = append(out, t)
	}
	return out
}
