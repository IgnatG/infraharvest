// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// How drift takes a change of a resource whose block makes it with count
// or for_each, as a root environments share writes the resources some
// environments only have (count = var.deploy_<feature> ? 1 : 0) and the
// resources of one shape (for_each over a map of each key's fields).

// eachInstance is the instance of a block with for_each a change is of:
// its key, and what the block's for_each is over.
type eachInstance struct {
	block   *hclsyntax.Block
	forEach hclsyntax.Expression
	key     string
}

// instances checks the count or for_each of block, the resource c is of:
// a count of at most one makes one instance, whose arguments are its own;
// a for_each, the instance at c's key, whose own values are only those
// each.value reads (see eachResolver). It returns why the block's instances
// can't be told apart, or "".
func (w *driftWalk) instances(block *hclsyntax.Block) string {
	if attr, ok := block.Body.Attributes["count"]; ok && !atMostOne(attr.Expr) {
		return "the block makes several resources with count: edit it by hand"
	}
	attr, ok := block.Body.Attributes["for_each"]
	if !ok {
		return ""
	}
	key, ok := forEachKey(w.c.Address)
	if !ok {
		return "the block makes several resources with for_each: edit it by hand"
	}
	w.each = &eachInstance{block: block, forEach: attr.Expr, key: key}
	return ""
}

// atMostOne reports whether expr, a count, is 0 or 1, or a conditional
// of the two, such as var.deploy_alerts ? 1 : 0.
func atMostOne(expr hclsyntax.Expression) bool {
	switch x := expr.(type) {
	case *hclsyntax.ParenthesesExpr:
		return atMostOne(x.Expression)
	case *hclsyntax.ConditionalExpr:
		return atMostOne(x.TrueResult) && atMostOne(x.FalseResult)
	}
	if len(expr.Variables()) > 0 {
		return false
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() || !v.IsKnown() || v.IsNull() || !v.Type().Equals(cty.Number) {
		return false
	}
	return v.Equals(cty.Zero).True() || v.Equals(cty.NumberIntVal(1)).True()
}

// forEachKey returns the string key of the instance at address, such as
// orders for aws_sqs_queue.this["orders"].
func forEachKey(address string) (string, bool) {
	t, diags := hclsyntax.ParseTraversalAbs([]byte(address), "", hcl.InitialPos)
	if diags.HasErrors() || len(t) != 3 {
		return "", false
	}
	idx, ok := t[2].(hcl.TraverseIndex)
	if !ok {
		return "", false
	}
	k, ok := keyOf(idx.Key)
	return k.key, ok && k.hasKey
}

// eachResolver is each.value.<field> in a resource's block with for_each:
// the field of the instance's key in the map the block's for_each is
// over, as the person who wrote the map edits it. The map is a local's
// object literal, or, in a root environments share, the value the
// environment's var file gives a map variable, or, where the file doesn't
// set it, the variable's default, which the edit then adds to the file
// with the key's field changed. Where the block reads the field more than
// once, or anything but the block and its import blocks refers to the
// map, an edit of it would reach more than the instance: a hand edit.
type eachResolver struct{}

func (s eachResolver) resolve(w *driftWalk, x *hclsyntax.ScopeTraversalExpr, path []string, i int) codeSource {
	ref := w.text(x.Range())
	field := ""
	if t := x.Traversal; len(t) == 3 {
		value, isAttr := t[1].(hcl.TraverseAttr)
		name, isField := t[2].(hcl.TraverseAttr)
		if isAttr && isField && value.Name == "value" {
			field = name.Name
		}
	}
	if field == "" {
		return w.manual(sourceExpression, x.Range(), ref+" sets it: edit it by hand")
	}
	if eachFieldUses(w.each.block, field) > 1 {
		return w.manual(sourceExpression, x.Range(), ref+", which other arguments of the block read too, sets it: edit it by hand")
	}
	over, ok := w.each.forEach.(*hclsyntax.ScopeTraversalExpr)
	name := ""
	if ok && len(over.Traversal) == 2 {
		if step, isAttr := over.Traversal[1].(hcl.TraverseAttr); isAttr {
			name = step.Name
		}
	}
	root := ""
	if name != "" {
		root = over.Traversal.RootName()
	}
	switch root {
	case "local":
		return s.overLocal(w, x, name, field, path, i)
	case "var":
		return s.overVariable(w, x, variableResolver{name: name, block: w.cfg.variables[name]}, field, path, i)
	}
	return w.manual(sourceExpression, x.Range(), "the block's for_each isn't a local or a variable: edit it by hand")
}

// overLocal is resolve for a for_each over local.name.
func (eachResolver) overLocal(w *driftWalk, x *hclsyntax.ScopeTraversalExpr, name, field string, path []string, i int) codeSource {
	source := "local." + name
	def, ok := w.cfg.locals[name]
	if !ok {
		return w.manual(sourceLocal, x.Range(), source+" sets it, which the root doesn't define: edit it by hand")
	}
	if w.cfg.uses[name] > blockUses(w.each.block, "local", name)+importUses(w.cfg, "local", name, w.address) {
		return w.manual(sourceSharedLocal, x.Range(), source+", which other blocks use too, sets it: edit it by hand")
	}
	inner := *w
	inner.file, inner.f, inner.depth = def.file, def.f, w.depth+1
	src := inner.entry(def.attr.Expr, source, field, path, i)
	if src.Kind == sourceLiteral || src.Kind == sourceAbsent {
		src.Kind = sourceLocal
	}
	return src
}

// overVariable is resolve for a for_each over v, a variable, whose
// reasons for a hand edit are the variable's own (see variableResolver).
func (eachResolver) overVariable(w *driftWalk, x *hclsyntax.ScopeTraversalExpr, v variableResolver, field string, path []string, i int) codeSource {
	source := "var." + v.name
	switch {
	case v.block == nil:
		return v.undeclared(w, x)
	case sensitiveVariable(v.block):
		return v.secret(w, x)
	case w.e.env == "":
		return v.given(w, x)
	case w.cfg.varUses[v.name] > blockUses(w.each.block, "var", v.name)+importUses(w.cfg, "var", v.name, w.address):
		return v.hand(w, x, fmt.Sprintf("%s, which other blocks use too, sets it: change it in %s by hand", source, w.e.envVars))
	}
	file := w.e.envVars
	vars := mergedVars(w.cfg.files)
	if vars.own(w.e.env) == nil {
		return v.noVarFile(w, x)
	}
	if val, ok := w.envValue(v.name); ok {
		inner := *w
		inner.file, inner.f = val.file, val.f
		src := inner.entry(val.attr.Expr, source+" in "+file, field, path, i)
		if src.Edit != nil || src.Kind == sourceLiteral || src.Kind == sourceAbsent {
			src.Kind = sourceVariable
		}
		return src
	}
	// The default holds the environment's value: the file gets it, with
	// the key's field changed.
	unset := v.hand(w, x, fmt.Sprintf("%s sets it, and %s doesn't give it a value: give it one by hand", source, file))
	def, ok := v.block.Body.Attributes["default"]
	vf := w.cfg.variableFile(v.block)
	if !ok || vf == nil {
		return unset
	}
	inner := *w
	inner.file, inner.f = vf.name, vf.f
	src := inner.entry(def.Expr, "the default of "+source, field, path, i)
	if src.Edit == nil {
		src.Kind = sourceVariable
		return src
	}
	rng := def.Expr.Range()
	edit := src.Edit
	if len(src.Also) > 0 || edit.File != vf.name || edit.Start < rng.Start.Byte || edit.End > rng.End.Byte {
		return unset
	}
	value := slices.Concat(vf.f.src[rng.Start.Byte:edit.Start], edit.Text, vf.f.src[edit.End:rng.End.Byte])
	set, _ := vars.set(w.e.env, v.name, value)
	return codeSource{File: w.file, Range: x.Range(), Kind: sourceVariable, instance: true, Edit: &set}
}

// entry follows the path from step i into field of the item at the
// instance's key of obj, the object literal source, the map for_each is
// over: as for any expression (see expr), adding the field where the key
// doesn't set it.
func (w *driftWalk) entry(obj hclsyntax.Expression, source, field string, path []string, i int) codeSource {
	m, ok := obj.(*hclsyntax.ObjectConsExpr)
	if !ok {
		return w.manual(sourceExpression, obj.Range(), source+" isn't an object literal: edit it by hand")
	}
	key := w.each.key
	item, ok := objectItem(m, key).(*hclsyntax.ObjectConsExpr)
	if !ok {
		return w.manual(sourceExpression, m.Range(), fmt.Sprintf("%s has no object literal at the key %q: edit it by hand", source, key))
	}
	var src codeSource
	set := false
	for j := range item.Items {
		if keyName(item.Items[j].KeyExpr) == field {
			src = w.expr(item.Items[j].ValueExpr, holder{}, path, i)
			set = true
			break
		}
	}
	switch {
	case set:
	case i == len(w.steps):
		src = w.insertKey(item, field, path, i)
	default:
		return w.manual(sourceExpression, item.Range(), fmt.Sprintf("%s doesn't set %s at the key %q: edit it by hand", source, field, key))
	}
	src.instance = src.Edit != nil
	return src
}

// eachFieldUses counts the references in block to each.value.<field>.
func eachFieldUses(block *hclsyntax.Block, field string) int {
	n := 0
	for _, t := range allTraversals(block.Body) {
		if t.RootName() != "each" || len(t) < 3 {
			continue
		}
		value, isValue := t[1].(hcl.TraverseAttr)
		name, isName := t[2].(hcl.TraverseAttr)
		if isValue && isName && value.Name == "value" && name.Name == field {
			n++
		}
	}
	return n
}

// importUses counts the references to root.name, such as var.sqs_queues,
// in the import blocks of cfg into the resource at address: those of a
// for_each's import block, for_each = var.sqs_queues, go with the block.
func importUses(cfg *driftConfig, root, name, address string) int {
	n := 0
	for _, f := range cfg.files {
		for _, b := range f.syntax.Blocks {
			if b.Type != "import" {
				continue
			}
			to, ok := b.Body.Attributes["to"]
			if !ok {
				continue
			}
			rng := to.Expr.Range()
			if rng.End.Byte > len(f.src) || trimInstanceKey(strings.Join(strings.Fields(string(f.src[rng.Start.Byte:rng.End.Byte])), "")) != address {
				continue
			}
			n += blockUses(b, root, name)
		}
	}
	return n
}

// ownBlock reports whether src's edits, of a root environments share,
// are all in the block of c's resource, where only the environment
// checked has it: its count is a bool variable's, count = var.<name> ? 1
// : 0, which no other environment sets true (see envOnlyFlags), as for a
// feature, or residue, only it has. The other environments have no
// instance of the block, so the edit reaches only this one's.
func (e *driftEditor) ownBlock(cfg *driftConfig, c *DriftChange, src codeSource) bool {
	if c.ModuleAddress != "" || c.shared != nil || src.Edit == nil {
		return false
	}
	b, ok := cfg.blocks[trimInstanceKey(c.Address)]
	if !ok {
		return false
	}
	count, ok := b.b.Body.Attributes["count"]
	if !ok {
		return false
	}
	cond, ok := count.Expr.(*hclsyntax.ConditionalExpr)
	if !ok || !atMostOne(cond) {
		return false
	}
	flag, ok := cond.Condition.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(flag.Traversal) != 2 || flag.Traversal.RootName() != "var" {
		return false
	}
	step, ok := flag.Traversal[1].(hcl.TraverseAttr)
	if !ok || !e.ownFlags[step.Name] {
		return false
	}
	rng := b.b.Range()
	for _, edit := range append([]codeEdit{*src.Edit}, src.Also...) {
		if edit.File != b.file || edit.Start < rng.Start.Byte || edit.End > rng.End.Byte {
			return false
		}
	}
	return true
}

// envOnlyFlags returns the bool variables, declared in files, of the root
// environments share in dir that environment env alone sets true, such as
// the deploy_ variables of the features, and residue, only it has (see
// mergedVars.ownFlags).
func envOnlyFlags(dir, env string, files []*hclFile) (map[string]bool, error) {
	envs, err := MergedEnvironments(dir)
	if err != nil {
		return nil, err
	}
	defaults := map[string]cty.Value{}
	for _, f := range files {
		for _, b := range f.syntax.Blocks {
			if b.Type != "variable" || len(b.Labels) != 1 {
				continue
			}
			typ, ok := b.Body.Attributes["type"]
			if !ok || hcl.ExprAsKeyword(typ.Expr) != "bool" {
				continue
			}
			value := cty.NullVal(cty.Bool)
			if def, ok := b.Body.Attributes["default"]; ok {
				v, diags := def.Expr.Value(nil)
				if diags.HasErrors() {
					continue
				}
				value = v
			}
			defaults[b.Labels[0]] = value
		}
	}
	vars, err := loadMergedVars(dir, append(envs, env))
	if err != nil {
		return nil, err
	}
	return vars.ownFlags(env, defaults), nil
}

// variableUses returns why var.name, which other arguments use too,
// can't take the cloud's value, c's, in the environment's var file, at
// the steps sub into the variable, such as a key of a map: what else
// refers to it, or an argument it sets as it is, in a resource of the
// environment, that doesn't have the cloud's value there. "" where every
// argument it reaches has it, as when they changed alike in the cloud:
// the var file's value then takes them all, each of their changes
// accepted with it, or rejected by the plan where it is kept (see
// judgeDrift). A resource the environment hasn't, where its block is
// counted, as a feature another environment has, isn't reached.
func (w *driftWalk) variableUses(name string, sub []pathStep) string {
	file := w.e.envVars
	shared := fmt.Sprintf("var.%s, which other arguments use too, sets it: change it in %s by hand if they should all take the cloud's value", name, file)
	for _, fileName := range sortedKeys(w.cfg.files) {
		if fileName == file {
			continue
		}
		f := w.cfg.files[fileName]
		for _, b := range f.syntax.Blocks {
			if b.Type == "variable" || b.Type == "output" || blockUses(b, "var", name) == 0 {
				continue
			}
			if b.Type != "resource" || len(b.Labels) != 2 {
				return shared
			}
			address := b.Labels[0] + "." + b.Labels[1]
			var paths []string
			if !varPaths(b.Body, "", name, &paths) {
				return shared
			}
			instances := 0
			for _, instance := range sortedKeys(w.e.current) {
				if trimInstanceKey(instance) != address {
					continue
				}
				instances++
				values := w.e.current[instance]
				if values == nil {
					return fmt.Sprintf("var.%s sets %s too, which the environment hasn't applied yet: change it in %s by hand if it should take the cloud's value", name, instance, file)
				}
				for _, p := range paths {
					v, _ := valueAt(values, splitPath(p, values))
					v, _ = valueAt(v, sub)
					if !reflect.DeepEqual(v, w.c.value) {
						return fmt.Sprintf("var.%s sets %s %s too, which doesn't have the cloud's value: change it in %s by hand if it should take it too", name, instance, p, file)
					}
				}
			}
			_, counted := b.Body.Attributes["count"]
			_, each := b.Body.Attributes["for_each"]
			if instances == 0 && !counted && !each {
				return fmt.Sprintf("var.%s sets %s too, which the environment's state doesn't have: change it in %s by hand if it should take the cloud's value", name, address, file)
			}
		}
	}
	return ""
}

// varPaths adds to paths the path of each argument of body, at prefix,
// that var.name sets as it is, or a key of whose object literal it sets,
// such as tags.Env; it reports false where anything else reads var.name,
// such as a template, a function or a dynamic block.
func varPaths(body *hclsyntax.Body, prefix, name string, paths *[]string) bool {
	for attrName, attr := range body.Attributes {
		if !exprVarPaths(attr.Expr, joinPath(prefix, attrName), name, paths) {
			return false
		}
	}
	counts := map[string]int{}
	for _, b := range body.Blocks {
		if b.Type == "dynamic" || b.Type == "lifecycle" {
			if blockUses(b, "var", name) > 0 {
				return false
			}
			continue
		}
		at := fmt.Sprintf("%s[%d]", joinPath(prefix, b.Type), counts[b.Type])
		counts[b.Type]++
		if !varPaths(b.Body, at, name, paths) {
			return false
		}
	}
	return true
}

// exprVarPaths is varPaths for expr, an argument's value at path.
func exprVarPaths(expr hclsyntax.Expression, path, name string, paths *[]string) bool {
	switch x := expr.(type) {
	case *hclsyntax.ScopeTraversalExpr:
		if len(x.Traversal) == 2 && x.Traversal.RootName() == "var" {
			if step, ok := x.Traversal[1].(hcl.TraverseAttr); ok && step.Name == name {
				*paths = append(*paths, path)
				return true
			}
		}
	case *hclsyntax.ObjectConsExpr:
		for _, item := range x.Items {
			key := keyName(item.KeyExpr)
			if key == "" {
				if usesVariable(item.KeyExpr, name) || usesVariable(item.ValueExpr, name) {
					return false
				}
				continue
			}
			if !exprVarPaths(item.ValueExpr, path+"."+key, name, paths) {
				return false
			}
		}
		return true
	}
	return !usesVariable(expr, name)
}

// usesVariable reports whether expr refers to var.name.
func usesVariable(expr hclsyntax.Expression, name string) bool {
	for _, t := range expr.Variables() {
		if t.RootName() != "var" || len(t) < 2 {
			continue
		}
		if step, ok := t[1].(hcl.TraverseAttr); ok && step.Name == name {
			return true
		}
	}
	return false
}

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}
