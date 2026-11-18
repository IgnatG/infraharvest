// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"slices"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// driftWalk follows a change's path through a resource's block.
type driftWalk struct {
	e       *driftEditor
	cfg     *driftConfig
	c       *Change
	file    string // of the expression walked
	f       *hclfile.File
	address string
	res     refreshedResource
	steps   []pathStep
	// attrAt is the index in steps after the argument's name.
	attrAt int
	depth  int // of locals followed
	// mod is set for the walk of a module call's argument, whose steps
	// aren't the resource's path (see callWalk).
	mod *callWalk
	// each is set for the walk of a resource's block with for_each: the
	// instance the change is of (see eachResolver).
	each *eachInstance
}

// holder is what holds an expression: an argument of a block, an item of
// an object, or neither, which can't be removed.
type holder struct {
	attr *hclsyntax.Attribute
	obj  *hclsyntax.ObjectConsExpr
	item *hclsyntax.ObjectConsItem
}

func (w *driftWalk) manual(kind string, rng hcl.Range, reason string) codeSource {
	return codeSource{File: w.file, Range: rng, Kind: kind, Reason: reason}
}

func (w *driftWalk) text(rng hcl.Range) string {
	if rng.End.Byte > len(w.f.Src) || rng.Start.Byte > rng.End.Byte {
		return ""
	}
	return string(w.f.Src[rng.Start.Byte:rng.End.Byte])
}

// at returns the cloud's value at the first n steps, or nil if the cloud
// has none; at the whole path, the change's own value. Of a module call's
// argument, it is nil where it isn't known (see known).
func (w *driftWalk) at(n int) any {
	if n >= len(w.steps) {
		return w.c.value
	}
	steps := w.steps[:n]
	if w.mod != nil {
		if !w.known(n) {
			return nil
		}
		steps = slices.Concat(w.mod.prefix, w.steps[w.mod.offset:n])
	}
	if v, ok := valueAt(w.res.after, steps); ok {
		return v
	}
	if m, ok := w.res.after.(map[string]any); ok && len(steps) > 0 {
		for from, to := range foldedAttributes {
			if to == steps[0].name {
				if v, ok := valueAt(map[string]any{to: m[from]}, steps); ok {
					return v
				}
			}
		}
	}
	return nil
}

// body follows the path from step i in block's body; names are the block
// types from the resource's block to block, for the schema.
func (w *driftWalk) body(block *hclsyntax.Block, names []string, i int) codeSource {
	s := w.steps[i]
	if s.isIndex {
		return w.manual(sourceExpression, block.DefRange(), "edit it by hand")
	}
	path := append(slices.Clone(names), s.name)
	if attr, ok := block.Body.Attributes[s.name]; ok {
		w.attrAt = i + 1
		return w.expr(attr.Expr, holder{attr: attr}, path, i+1)
	}
	var nested []*hclsyntax.Block
	for _, nb := range block.Body.Blocks {
		if nb.Type == "dynamic" && len(nb.Labels) == 1 && nb.Labels[0] == s.name {
			return w.manual(sourceExpression, nb.DefRange(), "a dynamic "+s.name+" block makes it: edit it by hand")
		}
		if nb.Type == s.name {
			nested = append(nested, nb)
		}
	}
	if len(nested) > 0 {
		j, next := 0, i+1
		if next < len(w.steps) && w.steps[next].isIndex {
			j, next = w.steps[next].index, next+1
		}
		if j >= len(nested) || next >= len(w.steps) {
			return w.manual(sourceExpression, nested[len(nested)-1].DefRange(), "a "+s.name+" block was added or removed in the cloud: edit the blocks by hand")
		}
		return w.body(nested[j], path, next)
	}
	if w.mod == nil && w.e.schemas != nil && schemaBlockType(w.e.schemas, w.c.Type, path) {
		return w.manual(sourceAbsent, block.DefRange(), "the code has no "+s.name+" block: add it by hand")
	}
	// An argument the code leaves out.
	w.attrAt = i + 1
	var value any
	var sub []pathStep
	switch {
	case i+2 == len(w.steps) && !w.steps[i+1].isIndex:
		// A key of a map, alone: the other keys the cloud has are other
		// changes, each decided on its own.
		if w.c.value != nil {
			value = map[string]any{w.steps[i+1].name: w.c.value}
		}
		if w.mod != nil && !w.mod.fits(s.name, value) {
			return w.manual(sourceAbsent, block.DefRange(), "the call doesn't set "+s.name+", whose type needs more than the cloud's value: add it by hand")
		}
	case !w.known(i + 1):
		return w.manual(sourceAbsent, block.DefRange(), "the call doesn't set "+s.name+", and the cloud's value is only part of it: add it by hand")
	default:
		value = w.at(i + 1)
	}
	if value == nil {
		return w.manual(sourceAbsent, block.DefRange(), "the code doesn't set it, and the cloud has no value for it")
	}
	text, err := w.valueText(value, path, sub)
	if err != nil {
		return w.manual(sourceAbsent, block.DefRange(), "the cloud's value can't be written: add it by hand")
	}
	edit := w.insertArgument(block, s.name, text)
	if edit == nil {
		return w.manual(sourceAbsent, block.DefRange(), "the block is on one line: add the argument by hand")
	}
	return codeSource{File: w.file, Range: block.DefRange(), Kind: sourceAbsent, Edit: edit}
}

// expr follows the path from step i in e, which h holds; path names the
// argument e is, or is in, for the schema.
func (w *driftWalk) expr(e hclsyntax.Expression, h holder, path []string, i int) codeSource {
	switch x := e.(type) {
	case *hclsyntax.ParenthesesExpr:
		return w.expr(x.Expression, holder{}, path, i)
	case *hclsyntax.ScopeTraversalExpr:
		return w.traversal(x, path, i)
	case *hclsyntax.ObjectConsExpr:
		if i == len(w.steps) {
			if literalExpr(x) {
				return w.replace(x, h, path, i)
			}
			return w.computed(x.Range())
		}
		s := w.steps[i]
		if !s.isIndex {
			for j := range x.Items {
				item := &x.Items[j]
				if hclfile.KeyName(item.KeyExpr) == s.name {
					return w.expr(item.ValueExpr, holder{obj: x, item: item}, path, i+1)
				}
			}
			if i+1 == len(w.steps) {
				return w.insertKey(x, s.name, path, i+1)
			}
		}
		if literalExpr(x) {
			return w.replace(x, h, path, i)
		}
		return w.computed(x.Range())
	case *hclsyntax.TupleConsExpr:
		// A list is written whole: the order of a set's elements is the
		// provider's, so its elements can't be told apart by index.
		if literalExpr(x) {
			return w.replace(x, h, path, i)
		}
		if i < len(w.steps) && w.steps[i].isIndex && w.steps[i].index < len(x.Exprs) {
			return w.expr(x.Exprs[w.steps[i].index], holder{}, path, i+1)
		}
		return w.computed(x.Range())
	case *hclsyntax.FunctionCallExpr:
		if x.Name == "merge" && i+1 == len(w.steps) && !w.steps[i].isIndex && len(x.Args) > 0 && !x.ExpandFinal {
			// The resource's own keys, which merge puts over the others.
			if last, ok := x.Args[len(x.Args)-1].(*hclsyntax.ObjectConsExpr); ok {
				return w.expr(last, holder{}, path, i)
			}
			return w.appendMergeArg(x, w.steps[i].name, path, i+1)
		}
		return w.manual(sourceExpression, x.Range(), x.Name+"() computes it: edit it by hand")
	case *hclsyntax.TemplateExpr:
		if !x.IsStringLiteral() {
			return w.manual(sourceExpression, x.Range(), "a template computes it: edit it by hand")
		}
		return w.replace(x, h, path, i)
	case *hclsyntax.LiteralValueExpr:
		return w.replace(x, h, path, i)
	}
	return w.computed(e.Range())
}

// literalExpr reports whether expr is written out, with no references and
// nothing computed, such as ["a", "b"] or { Env = "prod" }.
func literalExpr(expr hclsyntax.Expression) bool {
	if len(expr.Variables()) > 0 {
		return false
	}
	literal := true
	_ = hclsyntax.VisitAll(expr, func(n hclsyntax.Node) hcl.Diagnostics {
		switch t := n.(type) {
		case *hclsyntax.FunctionCallExpr, *hclsyntax.ConditionalExpr, *hclsyntax.ForExpr, *hclsyntax.SplatExpr:
			literal = false
		case *hclsyntax.TemplateExpr:
			if !t.IsStringLiteral() {
				literal = false
			}
		}
		return nil
	})
	return literal
}
