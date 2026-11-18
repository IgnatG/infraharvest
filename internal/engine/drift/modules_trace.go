// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"maps"
	"slices"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
)

// traced is where a moduleTracer got: the variable of the module, and the
// steps into its value, that the value at the tracer's steps comes from,
// and how many of those steps it took to get there, the others being the
// same in the variable's value; or, if it can't tell, why, at the
// expression that sets the value.
type traced struct {
	variable string
	sub      []pathStep
	at       int
	// file and rng are the expression that refers to the variable, or
	// sets the value, in the module.
	file   string
	rng    hcl.Range
	reason string
	// literal says the module sets the value to a literal.
	literal bool
}

// moduleTracer follows a path through a block of a module, a resource's
// or a call's of another module, to the variable it gets the value from:
// var.x, a key, attribute or element of it, such as var.x.k, var.x["k"],
// lookup(var.x, "k", ...) or try(var.x.k, ...), or a local that is one;
// in a block with count or for_each, also each.value, var.x[count.index]
// or var.x[each.key], at the instance's key.
type moduleTracer struct {
	m     *driftModule
	steps []pathStep
	// block is the block followed, and key its instance's key, for its
	// count or for_each.
	block *hclsyntax.Block
	key   instanceKey
}

func (t *moduleTracer) fail(file string, rng hcl.Range, reason string) traced {
	return traced{file: file, rng: rng, reason: reason}
}

// body follows the path from step i in block's body.
func (t *moduleTracer) body(file string, block *hclsyntax.Block, i int) traced {
	if i >= len(t.steps) || t.steps[i].isIndex {
		return t.fail(file, block.DefRange(), "the module sets it in a way that can't be traced")
	}
	s := t.steps[i]
	if attr, ok := block.Body.Attributes[s.name]; ok {
		return t.expr(file, attr.Expr, i+1, 0)
	}
	var nested []*hclsyntax.Block
	for _, nb := range block.Body.Blocks {
		if nb.Type == "dynamic" && len(nb.Labels) == 1 && nb.Labels[0] == s.name {
			return t.fail(file, nb.DefRange(), "a dynamic "+s.name+" block of the module makes it")
		}
		if nb.Type == s.name {
			nested = append(nested, nb)
		}
	}
	if len(nested) > 0 {
		j, next := 0, i+1
		if next < len(t.steps) && t.steps[next].isIndex {
			j, next = t.steps[next].index, next+1
		}
		if j >= len(nested) || next >= len(t.steps) {
			return t.fail(file, block.DefRange(), "the module has no "+s.name+" block for it")
		}
		return t.body(file, nested[j], next)
	}
	return t.fail(file, block.DefRange(), "the module doesn't set "+s.name)
}

// expr follows the path from step i in e; depth counts the locals
// followed.
func (t *moduleTracer) expr(file string, e hclsyntax.Expression, i, depth int) traced {
	if variable, sub, ok := t.ref(e, 0); ok {
		return traced{variable: variable, sub: sub, at: i, file: file, rng: e.Range()}
	}
	switch x := e.(type) {
	case *hclsyntax.ParenthesesExpr:
		return t.expr(file, x.Expression, i, depth)
	case *hclsyntax.ObjectConsExpr:
		if i < len(t.steps) && !t.steps[i].isIndex {
			for _, item := range x.Items {
				if hclfile.KeyName(item.KeyExpr) == t.steps[i].name {
					return t.expr(file, item.ValueExpr, i+1, depth)
				}
			}
		}
	case *hclsyntax.TupleConsExpr:
		if i < len(t.steps) && t.steps[i].isIndex && t.steps[i].index < len(x.Exprs) {
			return t.expr(file, x.Exprs[t.steps[i].index], i+1, depth)
		}
	case *hclsyntax.ScopeTraversalExpr:
		if def, ok := t.m.cfg.locals[localName(x)]; ok && depth < maxLocalDepth {
			return t.expr(def.file, def.attr.Expr, i, depth+1)
		}
	}
	if literalExpr(e) {
		r := t.fail(file, e.Range(), "")
		r.literal, r.at = true, i
		return r
	}
	return t.fail(file, e.Range(), "an expression in the module computes it")
}

// ref returns the variable, and the steps into it, whose value e is, as
// it is; depth counts the locals followed. Of a block that makes several
// instances, each.value is the instance's value of the variable for_each
// is, and count.index and each.key index the variable at the instance's
// key.
func (t *moduleTracer) ref(e hclsyntax.Expression, depth int) (string, []pathStep, bool) {
	switch x := e.(type) {
	case *hclsyntax.ParenthesesExpr:
		return t.ref(x.Expression, depth)
	case *hclsyntax.ScopeTraversalExpr:
		switch x.Traversal.RootName() {
		case "var":
			if len(x.Traversal) < 2 {
				return "", nil, false
			}
			name, ok := x.Traversal[1].(hcl.TraverseAttr)
			if !ok {
				return "", nil, false
			}
			sub, ok := traversalSteps(x.Traversal[2:])
			return name.Name, sub, ok
		case "local":
			def, ok := t.m.cfg.locals[localName(x)]
			if !ok || depth >= maxLocalDepth {
				return "", nil, false
			}
			return t.ref(def.attr.Expr, depth+1)
		case "each":
			if len(x.Traversal) < 2 || t.block == nil || !t.key.hasKey {
				return "", nil, false
			}
			forEach, ok := t.block.Body.Attributes["for_each"]
			if step, value := x.Traversal[1].(hcl.TraverseAttr); !ok || !value || step.Name != "value" {
				return "", nil, false
			}
			name, sub, ok := t.ref(forEach.Expr, depth)
			into, valid := traversalSteps(x.Traversal[2:])
			if !ok || !valid {
				return "", nil, false
			}
			if ty, typed := t.m.variables[name]; typed {
				// Of a set, each.value is the key, not a value in it.
				if ty = descendType(ty, sub); ty.IsSetType() || ty.IsListType() {
					return "", nil, false
				}
			}
			return name, slices.Concat(sub, []pathStep{{name: t.key.key}}, into), true
		}
	case *hclsyntax.RelativeTraversalExpr:
		name, sub, ok := t.ref(x.Source, depth)
		into, valid := traversalSteps(x.Traversal)
		if ok && valid {
			return name, slices.Concat(sub, into), true
		}
	case *hclsyntax.IndexExpr:
		name, sub, ok := t.ref(x.Collection, depth)
		if step, isKey := t.keyStep(x.Key); ok && isKey {
			return name, append(slices.Clone(sub), step), true
		}
	case *hclsyntax.FunctionCallExpr:
		switch {
		case x.ExpandFinal:
		case x.Name == "lookup" && (len(x.Args) == 2 || len(x.Args) == 3):
			name, sub, ok := t.ref(x.Args[0], depth)
			if step, isKey := t.keyStep(x.Args[1]); ok && isKey {
				return name, append(slices.Clone(sub), step), true
			}
		case x.Name == "try" && len(x.Args) > 0:
			return t.ref(x.Args[0], depth)
		}
	}
	return "", nil, false
}

// keyStep returns the step e, a key, takes: a literal key, or of a block
// that makes several instances, the instance's count.index or each.key.
func (t *moduleTracer) keyStep(e hclsyntax.Expression) (pathStep, bool) {
	if key := literalKey(e); key != "" {
		return pathStep{name: key}, true
	}
	x, ok := e.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(x.Traversal) != 2 || t.block == nil {
		return pathStep{}, false
	}
	step, ok := x.Traversal[1].(hcl.TraverseAttr)
	switch {
	case !ok:
	case x.Traversal.RootName() == "count" && step.Name == "index" && t.key.isNum:
		return pathStep{index: t.key.index, isIndex: true}, true
	case x.Traversal.RootName() == "each" && step.Name == "key" && t.key.hasKey:
		return pathStep{name: t.key.key}, true
	}
	return pathStep{}, false
}

// traversalSteps returns the steps of a relative traversal, such as .k or
// ["k"] or [0]; false for a splat or a key that isn't a literal.
func traversalSteps(t hcl.Traversal) ([]pathStep, bool) {
	var steps []pathStep
	for _, s := range t {
		switch s := s.(type) {
		case hcl.TraverseAttr:
			steps = append(steps, pathStep{name: s.Name})
		case hcl.TraverseIndex:
			k, ok := keyOf(s.Key)
			switch {
			case !ok:
				return nil, false
			case k.isNum:
				steps = append(steps, pathStep{index: k.index, isIndex: true})
			default:
				steps = append(steps, pathStep{name: k.key})
			}
		default:
			return nil, false
		}
	}
	return steps, true
}

// literalKey returns the string e is, if a literal, else "".
func literalKey(e hclsyntax.Expression) string {
	if len(e.Variables()) > 0 {
		return ""
	}
	v, diags := e.Value(nil)
	if diags.HasErrors() || !v.IsKnown() || v.IsNull() || v.Type() != cty.String {
		return ""
	}
	return v.AsString()
}

// inverseTrace finds, in a's Inverse, the input that sets the attribute
// at steps of the resource at inner, its address inside the module
// without instance keys, whose instance key is key.
func inverseTrace(a *adapters.Adapter, inner string, key instanceKey, steps []pathStep) traced {
	best, input, from := -1, "", ""
	for _, k := range slices.Sorted(maps.Keys(a.Inverse)) {
		target, attribute, ok := a.InverseTarget(k)
		if !ok || target != inner {
			continue
		}
		at := splitPath(attribute)
		if len(at) > len(steps) || len(at) <= best || !slices.Equal(at, steps[:len(at)]) {
			continue
		}
		best, input, from = len(at), a.Inverse[k], k
	}
	if best < 0 {
		return traced{reason: "its module, " + a.Source + " " + a.Version + ", sets it in a way drift doesn't trace back to the call's arguments"}
	}
	in, err := adapters.ParseInputPath(input)
	if err != nil {
		return traced{reason: err.Error()}
	}
	var sub []pathStep
	for _, s := range in.Steps {
		switch {
		case s.EachKey && !key.hasKey:
			return traced{reason: from + " has no for_each key"}
		case s.EachKey:
			sub = append(sub, pathStep{name: key.key})
		case s.IsIndex:
			sub = append(sub, pathStep{index: s.Index, isIndex: true})
		default:
			sub = append(sub, pathStep{name: s.Key})
		}
	}
	return traced{variable: in.Variable, sub: sub, at: best, reason: ""}
}

// callWalk is what a driftWalk of a module call's argument needs: the
// walk's steps are the argument's name, the steps into its value that the
// module reads the resource's value at, then the rest of the resource's
// path, the same in both.
type callWalk struct {
	module  *driftModule
	adapter *adapters.Adapter
	// prefix are the steps of the resource's path the module took, which
	// the walk's steps from offset on go on from.
	prefix []pathStep
	offset int
	// each counts the steps after the argument's name that go into a
	// value of the call's for_each, before its value.
	each int
}

// known reports whether the cloud's value at the first n steps is known:
// not of the call's argument above what the resource gets of it.
func (w *driftWalk) known(n int) bool {
	return w.mod == nil || n >= w.mod.offset
}

// unreadModule is why the module of key, a key of modules.json, couldn't
// be read (see loadDriftModules), with the run's secrets redacted; "" where
// nothing says.
func (e *driftEditor) unreadModule(key string) string {
	why, ok := e.unreadModules[key]
	if !ok {
		why = e.unreadModules[""]
	}
	return secret.RedactText(why, e.secretForms)
}
