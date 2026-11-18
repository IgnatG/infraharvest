// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// replace writes the cloud's value at step i over x, which h holds:
// removing the argument or the key if the cloud has none, or if it is
// the default the catalogue names for the argument.
func (w *driftWalk) replace(x hclsyntax.Expression, h holder, path []string, i int) codeSource {
	rng := x.Range()
	if !w.known(i) {
		return w.manual(sourceLiteral, rng, "it sets more than the cloud's value: edit it by hand")
	}
	value := w.at(i)
	src := codeSource{File: w.file, Range: rng, Kind: sourceLiteral}
	if value == nil {
		switch {
		case h.attr != nil && w.mod != nil:
			// A call that leaves the argument out gets the module's
			// default: the argument goes where that is none too, and is
			// set to null where it isn't, or can't be told.
			isNull, note := w.mod.nullDefault(h.attr.Name)
			if isNull && i == w.attrAt {
				src.Edit = w.removeLines(h.attr.SrcRange)
			}
			if src.Edit == nil {
				src.Edit = &hclfile.Edit{File: w.file, Start: rng.Start.Byte, End: rng.End.Byte, Text: []byte("null")}
				src.Note = note
			}
		case h.attr != nil:
			src.Edit = w.removeLines(h.attr.SrcRange)
		case h.item != nil:
			src.Edit = w.removeItem(h.obj, h.item)
		}
		if src.Edit == nil {
			return w.manual(sourceLiteral, rng, "the cloud has no value for it: edit it by hand")
		}
		return src
	}
	text, err := w.valueText(value, path, w.steps[min(w.attrAt, i):i])
	if err != nil {
		return w.unwritable(sourceLiteral, rng)
	}
	if h.attr != nil && i == w.attrAt && w.isDefault(path, text) {
		if edit := w.removeLines(h.attr.SrcRange); edit != nil {
			src.Edit = edit
			return src
		}
	}
	src.Edit = &hclfile.Edit{File: w.file, Start: rng.Start.Byte, End: rng.End.Byte, Text: text}
	return src
}

// isDefault reports whether text is the value the catalogue names as the
// default of the argument at path (see Rules.IsDefault).
func (w *driftWalk) isDefault(path []string, text []byte) bool {
	if w.mod != nil {
		// A call's argument: the default its adapter names, if curated.
		if w.mod.adapter == nil || len(path) != 1 {
			return false
		}
		tokens := hclfile.ParseExpr(string(text))
		if tokens == nil {
			return false
		}
		return w.mod.adapter.IsDefault(path[0], tokens)
	}
	if w.e.isDefault == nil {
		return false
	}
	expr, diags := hclsyntax.ParseExpression(text, "", hcl.InitialPos)
	return !diags.HasErrors() && w.e.isDefault(w.c.Type, strings.Join(path, "."), expr)
}

// literalKeys reports whether expr, an object or map literal, has the
// keys steps go through, but for the last, which may be one to add.
func literalKeys(expr hclsyntax.Expression, steps []pathStep) bool {
	for n, s := range steps {
		obj, ok := expr.(*hclsyntax.ObjectConsExpr)
		if !ok || s.isIndex {
			return false
		}
		var next hclsyntax.Expression
		for j := range obj.Items {
			if hclfile.KeyName(obj.Items[j].KeyExpr) == s.name {
				next = obj.Items[j].ValueExpr
				break
			}
		}
		if next == nil {
			return n == len(steps)-1
		}
		expr = next
	}
	return true
}

// sensitiveVariable reports whether a variable block says sensitive =
// true.
func sensitiveVariable(b *hclsyntax.Block) bool {
	attr, ok := b.Body.Attributes["sensitive"]
	if !ok {
		return false
	}
	v, diags := attr.Expr.Value(nil)
	return !diags.HasErrors() && v.Type() == cty.Bool && v.IsKnown() && !v.IsNull() && v.True()
}

// valueText writes value as HCL, typed as the schema types the argument
// at path and the steps sub into it: port = 80, not "80"; objects
// without their null attributes.
func (w *driftWalk) valueText(value any, path []string, sub []pathStep) ([]byte, error) {
	return typedValueText(value, w.typeAt(path, sub))
}

// typedValueText writes value as HCL, of type ty where it has one
// (cty.NilType for none), else as its JSON has it; objects without their
// null attributes.
func typedValueText(value any, ty cty.Type) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var v cty.Value
	if ty != cty.NilType && !ty.HasDynamicTypes() {
		v, err = ctyjson.Unmarshal(raw, ty)
	}
	if ty == cty.NilType || ty.HasDynamicTypes() || err != nil {
		implied, err := ctyjson.ImpliedType(raw)
		if err != nil {
			return nil, err
		}
		if v, err = ctyjson.Unmarshal(raw, implied); err != nil {
			return nil, err
		}
	}
	if !v.IsWhollyKnown() {
		return nil, errors.New("the value isn't known")
	}
	return hclfile.WithoutNullAttributes(hclwrite.TokensForValue(v)).Bytes(), nil
}

// typeAt returns the type of the argument at path, then of what the
// steps sub go into in it, or cty.NilType if the schemas don't say.
func (w *driftWalk) typeAt(path []string, sub []pathStep) cty.Type {
	if w.mod != nil {
		return w.mod.typeAt(w, w.attrAt+len(sub))
	}
	return descendType(tfrun.AttributeType(w.e.schemas, w.c.Type, path), sub)
}

// schemaBlockType reports whether the schemas have a nested block type at
// path in a resource of typ.
func schemaBlockType(schemas *tfjson.ProviderSchemas, typ string, path []string) bool {
	if schemas == nil || len(path) == 0 {
		return false
	}
	for _, provider := range schemas.Schemas {
		resource, ok := provider.ResourceSchemas[typ]
		if !ok || resource.Block == nil {
			continue
		}
		block := resource.Block
		for _, name := range path {
			nested, ok := block.NestedBlocks[name]
			if !ok || nested.Block == nil {
				return false
			}
			block = nested.Block
		}
		return true
	}
	return false
}

// keyText writes a map key: bare if it can be, else quoted.
func keyText(key string) string {
	if hclsyntax.ValidIdentifier(key) {
		return key
	}
	return string(hclwrite.TokensForValue(cty.StringVal(key)).Bytes())
}
