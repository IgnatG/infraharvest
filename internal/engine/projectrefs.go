// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// projectArguments are the module arguments that name a Google project,
// alone or in a list: the curated modules' project_id and projects.
var projectArguments = []string{"project", "project_id", "projects"}

// referToProjects makes the module calls in generated.tf that name a
// project the root manages as google_project refer to it, as a resource
// would: project_id = google_project.main.project_id, not the project's
// ID again. A call takes its arguments from the resources it replaces,
// which name the project by its ID; addReferencesSkipping doesn't refer
// them to it, as a resource's project usually goes to the provider block
// instead (see omitInherited). Only projectArguments count: another argument
// with the same value, such as a bucket's name, isn't the project. It
// reports whether it changed generated.tf.
func referToProjects(dir string) (bool, error) {
	generated, err := loadHCL(filepath.Join(dir, GeneratedFileName))
	if err != nil {
		return false, err
	}
	// The google_project resources, by their project ID.
	projects := map[string][]string{}
	for _, r := range generated.resources() {
		if AddressType(r.address) != "google_project" {
			continue
		}
		if attr, ok := r.syntax.Body.Attributes["project_id"]; ok {
			if id, ok := constantString(attr.Expr); ok && id != "" {
				projects[id] = append(projects[id], r.syntax.Labels[1])
			}
		}
	}
	if len(projects) == 0 {
		return false, nil
	}
	// reference returns the traversal that replaces id, if one project
	// has it.
	reference := func(id string) (hcl.Traversal, bool) {
		names := projects[id]
		if len(names) != 1 {
			return nil, false
		}
		return hcl.Traversal{hcl.TraverseRoot{Name: "google_project"}, hcl.TraverseAttr{Name: names[0]}, hcl.TraverseAttr{Name: "project_id"}}, true
	}
	changed := false
	blocks := generated.edit().Body().Blocks()
	for i, b := range generated.syntax.Blocks {
		if b.Type != "module" || len(b.Labels) != 1 || i >= len(blocks) {
			continue
		}
		body := blocks[i].Body()
		for _, name := range projectArguments {
			attr, ok := b.Body.Attributes[name]
			if !ok {
				continue
			}
			switch expr := attr.Expr.(type) {
			case *hclsyntax.TemplateExpr:
				if id, ok := literalString(expr); ok {
					if t, ok := reference(id); ok {
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
	if !changed {
		return false, nil
	}
	return true, generated.save()
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
		s, ok := literalString(template)
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
