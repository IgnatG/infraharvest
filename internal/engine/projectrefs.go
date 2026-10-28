// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"maps"
	"path/filepath"
	"slices"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// moduleTarget is a type whose resources module calls name by an
// attribute (see catalogue.ModuleReferences), with the labels of those
// generated.tf has, by the attribute's value.
type moduleTarget struct {
	refs    catalogue.ModuleReferences
	byValue map[string][]string
}

// referModulesToResources makes the module calls in generated.tf that name a
// resource the root manages by one of its attributes refer to it, as a
// resource would, where the catalogue says which arguments of a call name
// which (see catalogue.ModuleReferences): project_id =
// google_project.main.project_id, not the project's ID again. A call
// takes its arguments from the resources it replaces, which name the
// project by its ID; addReferencesSkipping doesn't refer them to it, as a
// resource's project usually goes to the provider block instead (see
// omitInherited). Only those arguments count: another argument with the
// same value, such as a bucket's name, isn't the project. It reports
// whether it changed generated.tf.
func referModulesToResources(dir string) (bool, error) {
	generated, err := hclfile.Load(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return false, err
	}
	// The resources module calls name, by type.
	targets := map[string]*moduleTarget{}
	for _, r := range generated.Resources() {
		typ := AddressType(r.Address)
		refs, ok := catalogue.ForType(typ).ModuleReferences(typ)
		if !ok {
			continue
		}
		if attr, ok := r.Syntax.Body.Attributes[refs.Attribute]; ok {
			if value, ok := hclfile.ConstantString(attr.Expr); ok && value != "" {
				if targets[typ] == nil {
					targets[typ] = &moduleTarget{refs: refs, byValue: map[string][]string{}}
				}
				targets[typ].byValue[value] = append(targets[typ].byValue[value], r.Syntax.Labels[1])
			}
		}
	}
	if len(targets) == 0 {
		return false, nil
	}
	changed := false
	blocks := generated.Edit().Body().Blocks()
	for i, b := range generated.Syntax.Blocks {
		if b.Type != "module" || len(b.Labels) != 1 || i >= len(blocks) {
			continue
		}
		body := blocks[i].Body()
		for _, typ := range slices.Sorted(maps.Keys(targets)) {
			target := targets[typ]
			// reference returns the traversal that replaces value, if one
			// resource has it.
			reference := func(value string) (hcl.Traversal, bool) {
				names := target.byValue[value]
				if len(names) != 1 {
					return nil, false
				}
				return hcl.Traversal{hcl.TraverseRoot{Name: typ}, hcl.TraverseAttr{Name: names[0]}, hcl.TraverseAttr{Name: target.refs.Attribute}}, true
			}
			for _, name := range target.refs.Arguments {
				attr, ok := b.Body.Attributes[name]
				if !ok {
					continue
				}
				switch expr := attr.Expr.(type) {
				case *hclsyntax.TemplateExpr:
					if value, ok := hclfile.LiteralString(expr); ok {
						if t, ok := reference(value); ok {
							body.SetAttributeTraversal(name, t)
							changed = true
						}
					}
				case *hclsyntax.TupleConsExpr:
					if tokens, ok := referInTuple(expr, reference); ok {
						body.SetAttributeRaw(name, tokens)
						changed = true
					}
				}
			}
		}
	}
	if !changed {
		return false, nil
	}
	return true, generated.Save()
}

// referInTuple returns a list of literal strings, such as a call's
// projects = ["acme-prod"], with each that reference finds replaced by
// its reference, and reports whether it replaced any. A list with
// anything but literal strings stays as it is.
func referInTuple(expr *hclsyntax.TupleConsExpr, reference func(string) (hcl.Traversal, bool)) (hclwrite.Tokens, bool) {
	elements := make([]hclwrite.Tokens, 0, len(expr.Exprs))
	replaced := false
	for _, e := range expr.Exprs {
		template, ok := e.(*hclsyntax.TemplateExpr)
		if !ok {
			return nil, false
		}
		s, ok := hclfile.LiteralString(template)
		if !ok {
			return nil, false
		}
		if t, ok := reference(s); ok {
			elements = append(elements, hclwrite.TokensForTraversal(t))
			replaced = true
			continue
		}
		elements = append(elements, hclwrite.TokensForValue(cty.StringVal(s)))
	}
	if !replaced {
		return nil, false
	}
	return hclwrite.TokensForTuple(elements), true
}
