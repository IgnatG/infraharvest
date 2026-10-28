// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// typeLiterals writes the literal values of the .tf files of dir as the
// types they are for: in resources and data sources, as their schemas
// have the arguments, and in calls of the modules Terraform installed, as
// the modules' variables have them. A number for a string argument
// becomes a string (port = "80"), a string for a number a number
// (bake_time_in_minutes = 0), and a string for a bool a bool, element by
// element in lists and sets. Terraform converts them all the same, so the
// plan stays as it was.
func typeLiterals(dir string, schemas *tfjson.ProviderSchemas) error {
	paths, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		return err
	}
	variables, err := moduleVariableTypes(dir)
	if err != nil {
		return err
	}
	for _, path := range paths {
		f, err := hclfile.Load(path)
		if err != nil {
			return err
		}
		changed := false
		blocks := f.Edit().Body().Blocks()
		for i, b := range f.Syntax.Blocks {
			if i >= len(blocks) {
				continue
			}
			switch {
			case (b.Type == "resource" || b.Type == "data") && len(b.Labels) == 2:
				typeOf := func(path []string) cty.Type { return schemaType(schemas, b.Type, b.Labels[0], path) }
				if typeIn(b.Body, blocks[i].Body(), nil, typeOf) {
					changed = true
				}
			case b.Type == "module" && len(b.Labels) == 1 && variables[b.Labels[0]] != nil:
				types := variables[b.Labels[0]]
				typeOf := func(path []string) cty.Type {
					if len(path) != 1 {
						return cty.NilType
					}
					return types[path[0]]
				}
				if typeIn(b.Body, blocks[i].Body(), nil, typeOf) {
					changed = true
				}
			}
		}
		if changed {
			if err := f.Save(); err != nil {
				return err
			}
		}
	}
	return nil
}

// typeIn writes the literal values of a body and its nested blocks, at
// prefix, as typeOf has their types, and reports whether it changed any.
func typeIn(syntax *hclsyntax.Body, write *hclwrite.Body, prefix []string, typeOf func(path []string) cty.Type) bool {
	changed := false
	for name, attr := range syntax.Attributes {
		path := append(append([]string(nil), prefix...), name)
		if v, ok := typedLiteral(attr.Expr, typeOf(path)); ok {
			write.SetAttributeValue(name, v)
			changed = true
		}
	}
	blocks := write.Blocks()
	for i, b := range syntax.Blocks {
		if i < len(blocks) && typeIn(b.Body, blocks[i].Body(), append(append([]string(nil), prefix...), b.Type), typeOf) {
			changed = true
		}
	}
	return changed
}

// typedLiteral returns expr's value as ty has it, if expr is a literal of
// another primitive type that converts to it, or a list of such, and
// reports whether it does.
func typedLiteral(expr hclsyntax.Expression, ty cty.Type) (cty.Value, bool) {
	if ty == cty.NilType || len(expr.Variables()) > 0 {
		return cty.NilVal, false
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.IsWhollyKnown() {
		return cty.NilVal, false
	}
	switch {
	case ty.IsPrimitiveType():
		return convertPrimitive(v, ty)
	case (ty.IsListType() || ty.IsSetType()) && ty.ElementType().IsPrimitiveType() && v.Type().IsTupleType():
		elements := make([]cty.Value, 0, v.LengthInt())
		changed := false
		for it := v.ElementIterator(); it.Next(); {
			_, element := it.Element()
			if converted, ok := convertPrimitive(element, ty.ElementType()); ok {
				element = converted
				changed = true
			}
			elements = append(elements, element)
		}
		if !changed {
			return cty.NilVal, false
		}
		return cty.TupleVal(elements), true
	}
	return cty.NilVal, false
}

// convertPrimitive returns v as ty, if v is a primitive value of another
// type that converts to it.
func convertPrimitive(v cty.Value, ty cty.Type) (cty.Value, bool) {
	if v.IsNull() || !v.Type().IsPrimitiveType() || v.Type().Equals(ty) || !ty.IsPrimitiveType() {
		return cty.NilVal, false
	}
	converted, err := convert.Convert(v, ty)
	if err != nil || converted.IsNull() {
		return cty.NilVal, false
	}
	return converted, true
}

// schemaType returns the type of the argument at path, nested block types
// then the argument, of a resource (mode resource) or data source (data)
// of typ, or cty.NilType if the schemas don't describe it.
func schemaType(schemas *tfjson.ProviderSchemas, mode, typ string, path []string) cty.Type {
	if schemas == nil || len(path) == 0 {
		return cty.NilType
	}
	for _, provider := range schemas.Schemas {
		schemasOf := provider.ResourceSchemas
		if mode == "data" {
			schemasOf = provider.DataSourceSchemas
		}
		resource, ok := schemasOf[typ]
		if !ok || resource.Block == nil {
			continue
		}
		block := resource.Block
		for _, name := range path[:len(path)-1] {
			nested, ok := block.NestedBlocks[name]
			if !ok || nested.Block == nil {
				return cty.NilType
			}
			block = nested.Block
		}
		if attr := block.Attributes[path[len(path)-1]]; attr != nil {
			return attr.AttributeType
		}
		return cty.NilType
	}
	return cty.NilType
}

// moduleVariableTypes returns the types of the variables of the modules
// the root in dir calls, by call and variable, as Terraform installed the
// modules (see installedModules).
func moduleVariableTypes(dir string) (map[string]map[string]cty.Type, error) {
	modules, err := installedModules(dir)
	if err != nil {
		return nil, err
	}
	types := map[string]map[string]cty.Type{}
	for _, m := range modules {
		files, err := configFiles(m.dir)
		if err != nil {
			continue
		}
		variables := map[string]cty.Type{}
		for _, f := range files {
			for _, b := range f.Syntax.Blocks {
				attr, ok := b.Body.Attributes["type"]
				if b.Type != "variable" || len(b.Labels) != 1 || !ok {
					continue
				}
				if ty, _, diags := typeexpr.TypeConstraintWithDefaults(attr.Expr); !diags.HasErrors() {
					variables[b.Labels[0]] = ty
				}
			}
		}
		types[m.key] = variables
	}
	return types, nil
}

// typeLiteralsOf types the literals of dir (see typeLiterals), with the
// providers' schemas tf has, which it reads only if dir has literals.
func typeLiteralsOf(ctx context.Context, tf tfrun.Terraform, dir string) error {
	files, err := configFiles(dir)
	if err != nil {
		return err
	}
	literals := false
	for _, f := range files {
		for _, b := range f.Syntax.Blocks {
			if (b.Type == "resource" || b.Type == "data" || b.Type == "module") && hasConstants(b.Body) {
				literals = true
			}
		}
	}
	if !literals {
		return nil
	}
	schemas, err := tf.ProvidersSchema(ctx)
	if err != nil {
		return fmt.Errorf("terraform providers schema: %w", err)
	}
	return typeLiterals(dir, schemas)
}
