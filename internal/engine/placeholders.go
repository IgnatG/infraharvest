// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// Placeholder is for configuration Terraform can't read back from the
// cloud, such as where a Lambda function's code comes from: when a
// resource sets none of OneOf, setPlaceholders sets the arguments in Set
// and makes Terraform ignore changes to Ignore, so the plan keeps what is
// deployed. Reason goes in a comment above ignore_changes.
type Placeholder struct {
	OneOf  []string
	Set    map[string]string
	Ignore []string
	Reason string
}

// placeholderField is an {argument} in a Placeholder value.
var placeholderField = regexp.MustCompile(`\{([a-z0-9_]+)\}`)

// setPlaceholders sets the placeholders of the resources in the file at
// path that need them (see Placeholder), and reports whether it changed
// anything.
func setPlaceholders(path string, placeholders map[string]Placeholder) (bool, error) {
	if len(placeholders) == 0 {
		return false, nil
	}
	return rewrite(path, func(f *hclwrite.File, _ *hclsyntax.Body) bool {
		changed := false
		for _, block := range f.Body().Blocks() {
			if block.Type() != "resource" || len(block.Labels()) != 2 {
				continue
			}
			p, ok := placeholders[block.Labels()[0]]
			if !ok || slices.ContainsFunc(p.OneOf, func(name string) bool { return isSet(block.Body(), name) }) {
				continue
			}
			setPlaceholder(block.Body(), block.Labels()[1], p)
			changed = true
		}
		return changed
	})
}

// isSet reports whether body sets argument name to something but null.
func isSet(body *hclwrite.Body, name string) bool {
	attr := body.GetAttribute(name)
	return attr != nil && !isNullExpr(attr.Expr().BuildTokens(nil))
}

func isNullExpr(tokens hclwrite.Tokens) bool {
	return len(tokens) == 1 && tokens[0].Type == hclsyntax.TokenIdent && string(tokens[0].Bytes) == "null"
}

// setPlaceholder sets p's arguments in body, a resource called label, and
// adds its ignore_changes to the resource's lifecycle block.
func setPlaceholder(body *hclwrite.Body, label string, p Placeholder) {
	for _, name := range slices.Sorted(maps.Keys(p.Set)) {
		value := placeholderField.ReplaceAllStringFunc(p.Set[name], func(field string) string {
			if v, ok := literalStringAttr(body, field[1:len(field)-1]); ok {
				return v
			}
			return label
		})
		body.SetAttributeValue(name, cty.StringVal(value))
	}
	lifecycle := lifecycleBlock(body)
	ignored := make([]hclwrite.Tokens, 0, len(p.Ignore))
	for _, name := range p.Ignore {
		ignored = append(ignored, hclwrite.TokensForIdentifier(name))
	}
	if p.Reason != "" {
		lifecycle.AppendUnstructuredTokens(hclwrite.Tokens{
			{Type: hclsyntax.TokenComment, Bytes: []byte("# " + strings.ToUpper(p.Reason[:1]) + p.Reason[1:] + ".\n")},
		})
	}
	lifecycle.SetAttributeRaw("ignore_changes", hclwrite.TokensForTuple(ignored))
}

// literalStringAttr returns the value of body's argument name if it is a
// literal string.
func literalStringAttr(body *hclwrite.Body, name string) (string, bool) {
	attr := body.GetAttribute(name)
	if attr == nil {
		return "", false
	}
	expr, diags := hclsyntax.ParseExpression(attr.Expr().BuildTokens(nil).Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() || len(expr.Variables()) > 0 {
		return "", false
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.IsKnown() || v.Type() != cty.String {
		return "", false
	}
	return v.AsString(), true
}
