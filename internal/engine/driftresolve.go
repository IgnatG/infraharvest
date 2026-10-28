// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// A sourceResolver is one kind of source an argument of the code gets its
// value from by a reference: a local (see localResolver), a variable (see
// variableResolver), the map a block's for_each is over (see eachResolver),
// or another resource (see referenceResolver). It says where that source
// sets the value a change's path goes to, and the edit that takes the
// cloud's value there, or why that takes a hand edit: each kind's
// reasons are its own, written in one place.
//
// locate finds the block a change is in, of the resource, of the root's
// module call or of the shared tags, and walks it (see driftWalk); the
// walk hands each reference it reaches to the resolver of its kind.
type sourceResolver interface {
	// resolve follows w's path from step i into the source x, a
	// reference, refers to; path names the argument x is, or is in, for
	// the schema.
	resolve(w *driftWalk, x *hclsyntax.ScopeTraversalExpr, path []string, i int) codeSource
}

// traversal follows the path from step i into what x refers to, through
// the resolver of its kind; what x is otherwise, such as count.index,
// computes the value.
func (w *driftWalk) traversal(x *hclsyntax.ScopeTraversalExpr, path []string, i int) codeSource {
	if r := w.resolver(x); r != nil {
		return r.resolve(w, x, path, i)
	}
	return w.computed(x.Range())
}

// resolver returns the resolver of what x refers to, or nil for what
// isn't a source of its own: a value an expression computes, such as
// count.index or path.module, or each.value outside the resource's own
// block with for_each.
func (w *driftWalk) resolver(x *hclsyntax.ScopeTraversalExpr) sourceResolver {
	name := ""
	if len(x.Traversal) == 2 {
		if step, ok := x.Traversal[1].(hcl.TraverseAttr); ok {
			name = step.Name
		}
	}
	switch x.Traversal.RootName() {
	case "local":
		if name != "" {
			return localResolver{name: name}
		}
	case "var":
		if name != "" {
			return variableResolver{name: name, block: w.cfg.variables[name]}
		}
	case "each":
		if w.each != nil && w.mod == nil {
			return eachResolver{}
		}
	case "count", "path", "terraform", "self":
	default:
		return referenceResolver{}
	}
	return nil
}

// computed is the source of what an expression at rng computes, which
// takes a hand edit.
func (w *driftWalk) computed(rng hcl.Range) codeSource {
	return w.manual(sourceExpression, rng, "an expression computes it: edit it by hand")
}

// unwritable is the source at rng, of kind, whose cloud's value can't be
// written as HCL, which takes a hand edit.
func (w *driftWalk) unwritable(kind string, rng hcl.Range) codeSource {
	return w.manual(kind, rng, "the cloud's value can't be written: edit it by hand")
}

// maxLocalDepth bounds how many locals referring to locals resolve
// follows.
const maxLocalDepth = 4

// localResolver is local.name: followed into its value where this resource
// alone uses it; else the resource gets its own value, merged over the
// local's for a key of it, as the other resources keep theirs.
type localResolver struct{ name string }

func (l localResolver) resolve(w *driftWalk, x *hclsyntax.ScopeTraversalExpr, path []string, i int) codeSource {
	ref := w.text(x.Range())
	def, ok := w.cfg.locals[l.name]
	if !ok || w.depth >= maxLocalDepth {
		return w.manual(sourceLocal, x.Range(), ref+" sets it: edit it by hand")
	}
	if w.cfg.uses[l.name] <= 1 {
		inner := *w
		inner.file, inner.f, inner.depth = def.file, def.f, w.depth+1
		src := inner.expr(def.attr.Expr, holder{}, path, i)
		if src.Kind == sourceLiteral || src.Kind == sourceAbsent {
			src.Kind = sourceLocal
		}
		return src
	}
	shared := func(why string) codeSource {
		return w.manual(sourceSharedLocal, x.Range(), ref+", which other resources use too, "+why+": edit it by hand")
	}
	var text []byte
	var err error
	if i+1 == len(w.steps) && !w.steps[i].isIndex {
		if w.c.value == nil {
			return shared("has the key the cloud removed")
		}
		if text, err = w.valueText(w.c.value, path, w.steps[min(w.attrAt, i+1):i+1]); err != nil {
			return w.unwritable(sourceSharedLocal, x.Range())
		}
		text = fmt.Appendf(nil, "merge(%s, { %s = %s })", ref, keyText(w.steps[i].name), text)
	} else {
		value := w.at(i)
		if value == nil || !w.known(i) {
			return shared("sets it, and the cloud has no value for all of it")
		}
		if text, err = w.valueText(value, path, w.steps[min(w.attrAt, i):i]); err != nil {
			return w.unwritable(sourceSharedLocal, x.Range())
		}
	}
	return codeSource{File: w.file, Range: x.Range(), Kind: sourceSharedLocal, Edit: &hclfile.Edit{File: w.file, Start: x.Range().Start.Byte, End: x.Range().End.Byte, Text: text}}
}

// variableResolver is var.name, declared by block, nil where the root
// doesn't declare it. Its value is the root's input, so the code isn't
// edited for it, but for a root environments share, where it is edited
// only in the environment's var file (see env). A block's for_each over
// it is the same source (see eachResolver.overVariable).
type variableResolver struct {
	name  string
	block *hclsyntax.Block
}

func (v variableResolver) resolve(w *driftWalk, x *hclsyntax.ScopeTraversalExpr, path []string, i int) codeSource {
	switch {
	case v.block == nil:
		return v.undeclared(w, x)
	case sensitiveVariable(v.block):
		return v.secret(w, x)
	case w.e.env != "":
		return v.env(w, x, path, i)
	case hasDefault(v.block):
		return v.hand(w, x, "var."+v.name+" sets it, with another root's value as its default: update it from that root")
	}
	return v.given(w, x)
}

// hand is the source x, var.name, is where why says it takes a hand edit.
func (v variableResolver) hand(w *driftWalk, x *hclsyntax.ScopeTraversalExpr, why string) codeSource {
	return w.manual(sourceVariable, x.Range(), why)
}

// undeclared is the source x is where the root doesn't declare the
// variable.
func (v variableResolver) undeclared(w *driftWalk, x *hclsyntax.ScopeTraversalExpr) codeSource {
	return v.hand(w, x, "var."+v.name+" sets it, which the root doesn't declare: edit it by hand")
}

// secret is the source x is where the variable is sensitive: its value
// is updated where it comes from, never written.
func (v variableResolver) secret(w *driftWalk, x *hclsyntax.ScopeTraversalExpr) codeSource {
	return w.manual(sourceSensitiveVariable, x.Range(), fmt.Sprintf("a secret, which var.%s sets: update it in your secret store, or TF_VAR_%s", v.name, v.name))
}

// given is the source x is where the root, not of environments, is given
// the variable's value.
func (v variableResolver) given(w *driftWalk, x *hclsyntax.ScopeTraversalExpr) codeSource {
	return v.hand(w, x, "var."+v.name+" sets it: change the value the root is given")
}

// noVarFile is the source x is where the environment has no var file of
// its own to set the variable in.
func (v variableResolver) noVarFile(w *driftWalk, x *hclsyntax.ScopeTraversalExpr) codeSource {
	return v.hand(w, x, fmt.Sprintf("var.%s sets it, and the root has no %s: give it the value by hand", v.name, w.e.envVars))
}

// moreThanCloud is the source x is where the variable sets more than
// what changed, which its var file's value doesn't let be told apart.
func (v variableResolver) moreThanCloud(w *driftWalk, x *hclsyntax.ScopeTraversalExpr) codeSource {
	return v.hand(w, x, fmt.Sprintf("var.%s sets more than the cloud's value: change it in %s by hand", v.name, w.e.envVars))
}

// env is the source of what x, var.name, sets from step i of the path in
// a root environments share: the value the environment's var file gives
// the variable, or, where the file doesn't set it, x, with an edit that
// adds it to the file. The value is set only where it is the whole of
// what changed, or a key of the object or map literal the file sets the
// variable to (see envKey), written as the schema types the argument at
// path, and every other argument that refers to the variable, whose
// value would reach it too, has the cloud's value as well in the
// environment (see variableUses).
func (v variableResolver) env(w *driftWalk, x *hclsyntax.ScopeTraversalExpr, path []string, i int) codeSource {
	file := w.e.envVars
	vars := mergedVars(w.cfg.files)
	switch {
	case vars.own(w.e.env) == nil:
		return v.noVarFile(w, x)
	case w.mod != nil:
		return v.moreThanCloud(w, x)
	case i != len(w.steps):
		return v.envKey(w, x, path, i)
	case w.c.value == nil:
		return v.hand(w, x, fmt.Sprintf("var.%s sets it, and the cloud has no value for it: change it in %s by hand", v.name, file))
	}
	if w.cfg.varUses[v.name] > 1 {
		if reason := w.variableUses(v.name, nil); reason != "" {
			return v.hand(w, x, reason)
		}
	}
	text, err := w.valueText(w.c.value, path, w.steps[min(w.attrAt, i):i])
	if err != nil {
		return v.hand(w, x, fmt.Sprintf("the cloud's value can't be written: change var.%s in %s by hand", v.name, file))
	}
	edit, _ := vars.set(w.e.env, v.name, text)
	if val, ok := w.envValue(v.name); ok {
		return codeSource{File: file, Range: val.attr.Expr.Range(), Kind: sourceVariable, Edit: &edit}
	}
	return codeSource{File: w.file, Range: x.Range(), Kind: sourceVariable, Edit: &edit}
}

// envKey is env for what var.name sets below step i of the path, such as
// the owner key of tags = var.tags: the element of the object or map
// literal the environment's var file sets the variable to, edited alone
// with the cloud's value, or the key added to it, keeping the rest. A
// hand edit where the file doesn't set the variable, as when its default
// holds the environment's value, or sets it to anything but a literal
// with the keys the path goes through.
func (v variableResolver) envKey(w *driftWalk, x *hclsyntax.ScopeTraversalExpr, path []string, i int) codeSource {
	file := w.e.envVars
	val, ok := w.envValue(v.name)
	switch {
	case !ok:
		return v.hand(w, x, fmt.Sprintf("var.%s sets more than the cloud's value, and %s doesn't set it: add it to the file by hand", v.name, file))
	case !literalExpr(val.attr.Expr) || !literalKeys(val.attr.Expr, w.steps[i:]):
		return v.moreThanCloud(w, x)
	}
	if w.cfg.varUses[v.name] > 1 {
		if reason := w.variableUses(v.name, w.steps[i:]); reason != "" {
			return v.hand(w, x, reason)
		}
	}
	inner := *w
	inner.file, inner.f = val.file, val.f
	src := inner.expr(val.attr.Expr, holder{}, path, i)
	src.Kind = sourceVariable
	if src.Edit == nil && src.Reason != "" {
		src.Reason = fmt.Sprintf("var.%s sets it, in %s: %s", v.name, file, src.Reason)
	}
	return src
}

// envValue returns the argument the environment's own var file sets
// var.name to, in a root environments share; false where it doesn't, as
// where vars/common.tfvars or the variable's default holds the value,
// which drift doesn't edit for one environment.
func (w *driftWalk) envValue(name string) (varValue, bool) {
	v, ok := mergedVars(w.cfg.files).value(w.e.env, name)
	return v, ok && v.file == w.e.envVars
}

// referenceResolver is another resource, or a data source, an argument
// refers to: if the cloud's value is another managed resource's ID, ARN
// or name, a reference to it takes it; else it takes a hand edit.
type referenceResolver struct{}

func (referenceResolver) resolve(w *driftWalk, x *hclsyntax.ScopeTraversalExpr, path []string, i int) codeSource {
	ref := w.text(x.Range())
	if value, ok := w.c.value.(string); ok && value != "" && i == len(w.steps) {
		if target, ok := uniqueTarget(path[len(path)-1], value, w.e.refs[value]); ok && target.address != w.address {
			text := hclwrite.TokensForTraversal(target.traversal()).Bytes()
			if string(text) != ref {
				return codeSource{File: w.file, Range: x.Range(), Kind: sourceReference, Edit: &hclfile.Edit{File: w.file, Start: x.Range().Start.Byte, End: x.Range().End.Byte, Text: text}}
			}
		}
	}
	return w.manual(sourceReference, x.Range(), "it refers to "+ref+", and the cloud's value isn't another managed resource's: edit it by hand")
}
