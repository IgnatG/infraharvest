// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

// OmitArguments removes from the resources in path the arguments omit
// names for their type, or under "*" for every type, whether written as
// attributes or as blocks. An entry name=value removes the attribute only
// where it is set to that value (see omission). It reports whether it
// removed anything.
func OmitArguments(path string, omit map[string][]string) (bool, error) {
	if len(omit) == 0 {
		return false, nil
	}
	return rewrite(path, func(f *hclwrite.File, syntax *hclsyntax.Body) bool {
		changed := false
		for i, block := range f.Body().Blocks() {
			if block.Type() != "resource" || len(block.Labels()) != 2 || i >= len(syntax.Blocks) {
				continue
			}
			body := block.Body()
			attributes := syntax.Blocks[i].Body.Attributes
			for _, o := range omissions(omit, block.Labels()[0]) {
				if attr, ok := attributes[o.name]; ok && o.matches(attr.Expr) && body.RemoveAttribute(o.name) != nil {
					changed = true
				}
				if !o.always {
					continue
				}
				for _, nested := range body.Blocks() {
					if nested.Type() == o.name && body.RemoveBlock(nested) {
						changed = true
					}
				}
			}
		}
		return changed
	})
}

// omission is an entry of a catalogue's omit list: name, which leaves the
// argument out whatever its value, or name=value, which leaves the
// attribute out only where it is set to value, such as a default the
// cloud reports that the provider rejects as an argument.
type omission struct {
	name   string
	value  string
	always bool
}

// omissions returns the entries of omit for every type and for typ.
func omissions(omit map[string][]string, typ string) []omission {
	var list []omission
	for _, entry := range append(append([]string(nil), omit["*"]...), omit[typ]...) {
		name, value, withValue := strings.Cut(entry, "=")
		list = append(list, omission{name: name, value: value, always: !withValue})
	}
	return list
}

// matches reports whether an attribute set to expr is to be left out:
// always, or when expr is a constant whose value, as a string (true,
// 3, "text"), is o's value.
func (o omission) matches(expr hclsyntax.Expression) bool {
	if o.always {
		return true
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.IsWhollyKnown() {
		return false
	}
	s, err := convert.Convert(v, cty.String)
	if err != nil || s.IsNull() {
		return false
	}
	return s.AsString() == o.value
}
