// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"math/big"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
)

// driftModule is a module a root calls, as Terraform installed it, read
// to trace a change of one of its resources back to the call.
type driftModule struct {
	// dir is the module's directory, relative to the root.
	dir string
	cfg *driftConfig
	// variables are the types of its variables that declare one, with
	// their optional attributes, by name.
	variables map[string]cty.Type
	// calls counts the module calls, of the root and of its modules, that
	// call the directory.
	calls int
}

// loadDriftModules reads the modules the root in dir calls, by their keys
// in the modules.json of its last init, such as logs, or net.vpc for a
// call inside a module: none if it calls none, or if they can't be read.
func loadDriftModules(dir string) map[string]*driftModule {
	installed, err := installedModules(dir)
	if err != nil {
		return nil
	}
	calls := map[string]int{}
	for _, m := range installed {
		calls[filepath.Clean(m.dir)]++
	}
	modules := map[string]*driftModule{}
	for _, m := range installed {
		contents, err := tfContents(m.dir)
		if err != nil || len(contents) == 0 {
			continue
		}
		mod, err := newDriftModule(contents)
		if err != nil {
			continue
		}
		mod.dir = filepath.ToSlash(m.dir)
		if rel, err := filepath.Rel(dir, m.dir); err == nil {
			mod.dir = filepath.ToSlash(rel)
		}
		mod.calls = calls[filepath.Clean(m.dir)]
		modules[m.key] = mod
	}
	return modules
}

// newDriftModule parses a module's files, contents by name.
func newDriftModule(contents map[string][]byte) (*driftModule, error) {
	cfg, err := parseDriftConfig(contents)
	if err != nil {
		return nil, err
	}
	m := &driftModule{cfg: cfg, variables: map[string]cty.Type{}, calls: 1}
	for name, b := range cfg.variables {
		attr, ok := b.Body.Attributes["type"]
		if !ok {
			continue
		}
		if ty, _, diags := typeexpr.TypeConstraintWithDefaults(attr.Expr); !diags.HasErrors() {
			m.variables[name] = ty
		}
	}
	return m, nil
}

// instanceKey is the instance key of a resource or a module call: a
// for_each key, a count index, or none.
type instanceKey struct {
	key           string
	index         int
	hasKey, isNum bool
}

// moduleStep is a module call of a resource's address, with its instance
// key.
type moduleStep struct {
	name string
	key  instanceKey
}

// splitModuleAddress splits the address of a resource in a module call,
// such as module.logs["a.b"].aws_s3_bucket.this[0], into its calls, from
// the root's, the resource's address in the innermost module without its
// instance key, such as aws_s3_bucket.this, and that key; false for an
// address in no module call, or one it can't read.
func splitModuleAddress(address string) ([]moduleStep, string, instanceKey, bool) {
	t, diags := hclsyntax.ParseTraversalAbs([]byte(address), "", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, "", instanceKey{}, false
	}
	name := func(i int) string {
		if i >= len(t) {
			return ""
		}
		switch s := t[i].(type) {
		case hcl.TraverseRoot:
			return s.Name
		case hcl.TraverseAttr:
			return s.Name
		}
		return ""
	}
	key := func(i int) (instanceKey, bool) {
		if i >= len(t) {
			return instanceKey{}, false
		}
		idx, ok := t[i].(hcl.TraverseIndex)
		if !ok {
			return instanceKey{}, false
		}
		return keyOf(idx.Key)
	}
	var calls []moduleStep
	i := 0
	for name(i) == "module" && name(i+1) != "" {
		call := moduleStep{name: name(i + 1)}
		i += 2
		if k, ok := key(i); ok {
			call.key = k
			i++
		}
		calls = append(calls, call)
	}
	typ, n := name(i), name(i+1)
	if len(calls) == 0 || typ == "" || n == "" || typ == "data" {
		return nil, "", instanceKey{}, false
	}
	var k instanceKey
	switch {
	case i+2 == len(t):
	case i+3 == len(t):
		var ok bool
		if k, ok = key(i + 2); !ok {
			return nil, "", instanceKey{}, false
		}
	default:
		return nil, "", instanceKey{}, false
	}
	return calls, typ + "." + n, k, true
}

// keyOf returns an instance key's value as an instanceKey.
func keyOf(v cty.Value) (instanceKey, bool) {
	switch {
	case !v.IsKnown() || v.IsNull():
		return instanceKey{}, false
	case v.Type() == cty.String:
		return instanceKey{key: v.AsString(), hasKey: true}, true
	case v.Type() == cty.Number:
		i, accuracy := v.AsBigFloat().Int64()
		if accuracy != big.Exact || i < 0 {
			return instanceKey{}, false
		}
		return instanceKey{index: int(i), isNum: true}, true
	}
	return instanceKey{}, false
}

// moduleKey is the key modules.json has the innermost of calls by, such
// as net.vpc.
func moduleKey(calls []moduleStep) string {
	names := make([]string, len(calls))
	for i, c := range calls {
		names[i] = c.name
	}
	return strings.Join(names, ".")
}

// unkeyedAddress returns the address of the block of the resource at
// address, without the instance keys of the resource and of the module
// calls it is in, such as module.logs.aws_s3_bucket.this for
// module.logs["a"].aws_s3_bucket.this[0].
func unkeyedAddress(address string) string {
	calls, resource, _, ok := splitModuleAddress(address)
	if !ok {
		return trimInstanceKey(address)
	}
	var b strings.Builder
	for _, c := range calls {
		b.WriteString("module." + c.name + ".")
	}
	return b.String() + resource
}

// curatedAdapter returns the adapter whose module and exact version a
// module call calls, or nil.
func curatedAdapter(b *hclsyntax.Block) *adapters.Adapter {
	source, version := stringAttribute(b.Body, "source"), stringAttribute(b.Body, "version")
	if source == "" {
		return nil
	}
	for _, provider := range adapters.Providers() {
		list := adapters.For(provider)
		for i := range list {
			if list[i].Source == source && list[i].Version == version {
				return &list[i]
			}
		}
	}
	return nil
}

// hasInstances reports whether a block makes several instances, with
// count or for_each.
func hasInstances(b *hclsyntax.Block) bool {
	_, count := b.Body.Attributes["count"]
	_, forEach := b.Body.Attributes["for_each"]
	return count || forEach
}

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
// lookup(var.x, "k", ...) or try(var.x.k, ...), or a local that is one.
type moduleTracer struct {
	m     *driftModule
	steps []pathStep
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
				if keyName(item.KeyExpr) == t.steps[i].name {
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
		r.literal = true
		return r
	}
	return t.fail(file, e.Range(), "an expression in the module computes it")
}

// ref returns the variable, and the steps into it, whose value e is, as
// it is; depth counts the locals followed.
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
		}
	case *hclsyntax.IndexExpr:
		name, sub, ok := t.ref(x.Collection, depth)
		if key := literalKey(x.Key); ok && key != "" {
			return name, append(slices.Clone(sub), pathStep{name: key}), true
		}
	case *hclsyntax.FunctionCallExpr:
		switch {
		case x.ExpandFinal:
		case x.Name == "lookup" && (len(x.Args) == 2 || len(x.Args) == 3):
			name, sub, ok := t.ref(x.Args[0], depth)
			if key := literalKey(x.Args[1]); ok && key != "" {
				return name, append(slices.Clone(sub), pathStep{name: key}), true
			}
		case x.Name == "try" && len(x.Args) > 0:
			return t.ref(x.Args[0], depth)
		}
	}
	return "", nil, false
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
	for _, k := range sortedKeys(a.Inverse) {
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

// locateInModule finds where the root's code sets c's attribute of a
// resource in a module call, and the edit that takes the cloud's value:
// in the argument of the root's call that the module sets it from, as
// its code says (see moduleTracer), followed through the calls of other
// modules inside it, or, in a curated module, as its adapter's Inverse
// says; then as for a resource's argument (see driftWalk) in that
// argument. Nothing under the modules is ever edited.
func (e *driftEditor) locateInModule(cfg *driftConfig, c *DriftChange) Source {
	manual := func(reason string) Source {
		return Source{Kind: SourceModule, Reason: "in the module call " + c.ModuleAddress + ": " + reason}
	}
	calls, resource, key, ok := splitModuleAddress(c.Address)
	if !ok {
		return manual("its address can't be read: edit the call by hand")
	}
	blocks := make([]driftBlock, len(calls))
	mods := make([]*driftModule, len(calls))
	parent := cfg
	for i, call := range calls {
		b, ok := parent.calls[call.name]
		if !ok {
			return manual("the code has no block for module." + call.name + ": edit it by hand")
		}
		blocks[i] = b
		if mods[i] = e.modules[moduleKey(calls[:i+1])]; mods[i] == nil {
			src := manual("modules.json doesn't list the module, so what it sets can't be told: change the call by hand")
			src.File, src.Range = blocks[0].file, blocks[0].b.DefRange()
			return src
		}
		parent = mods[i].cfg
	}
	callSource := func(reason string) Source {
		src := manual(reason)
		src.File, src.Range = blocks[0].file, blocks[0].b.DefRange()
		return src
	}
	if c.Kind == DriftDeleted {
		return callSource("deleted in the cloud: change the call by hand, or apply to create it again")
	}
	steps := splitPath(c.Attribute, e.pathValues(c.Address)...)
	if len(steps) == 0 || steps[0].isIndex {
		return callSource("edit the call by hand")
	}

	// The module that sets the attribute: the innermost, or the curated
	// one it is in.
	level := len(calls) - 1
	var adapter *adapters.Adapter
	var t traced
	var via []string
	for i := range calls {
		if adapter = curatedAdapter(blocks[i].b); adapter != nil {
			var inner strings.Builder
			for j := i + 1; j < len(calls); j++ {
				if hasInstances(blocks[j].b) {
					return callSource("its module, " + adapter.Source + ", calls module." + calls[j].name + " for several instances: edit the call by hand")
				}
				inner.WriteString("module." + calls[j].name + ".")
			}
			inner.WriteString(resource)
			if t = inverseTrace(adapter, inner.String(), key, steps); t.reason != "" {
				return callSource(t.reason + ": edit the call by hand")
			}
			via = append(via, fmt.Sprintf("%s %s: %s.%s from %s", adapter.Source, adapter.Version, inner.String(), pathText(steps[:t.at]), inputText(t)))
			level = i
			break
		}
	}
	if adapter == nil {
		m := mods[level]
		b, ok := m.cfg.blocks[resource]
		if !ok {
			return callSource("the module " + m.dir + " has no block for " + resource + ": edit it by hand")
		}
		tr := &moduleTracer{m: m, steps: steps}
		if t = tr.body(b.file, b.b, 0); t.variable == "" {
			return e.moduleManual(c, m, t)
		}
		via = append(via, e.viaText(m, t))
	}
	// Up through the calls of modules inside modules, to the root's.
	rest := steps[t.at:]
	for i := level; i > 0; i-- {
		if hasInstances(blocks[i].b) {
			return callSource("module." + calls[i].name + " in the module " + mods[i-1].dir + " makes several instances: edit it by hand")
		}
		callSteps := slices.Concat([]pathStep{{name: t.variable}}, t.sub, rest)
		tr := &moduleTracer{m: mods[i-1], steps: callSteps}
		if t = tr.body(blocks[i].file, blocks[i].b, 0); t.variable == "" {
			return e.moduleManual(c, mods[i-1], t)
		}
		via = append(via, e.viaText(mods[i-1], t))
		rest = callSteps[t.at:]
	}
	slices.Reverse(via)

	b := blocks[0]
	w := &driftWalk{e: e, cfg: cfg, c: c, file: b.file, f: b.f, address: "module." + calls[0].name, res: e.resources[c.Address]}
	w.steps = slices.Concat([]pathStep{{name: t.variable}}, t.sub, rest)
	w.mod = &callWalk{module: mods[0], adapter: adapter, prefix: steps[:len(steps)-len(rest)], offset: 1 + len(t.sub)}
	if level > 0 {
		w.mod.adapter = nil
	}
	var src Source
	if hasInstances(b.b) {
		src = w.instance(b.b, calls[0].key)
	} else {
		src = w.body(b.b, nil, 0)
	}
	src.Module = strings.Join(via, "; ")
	if src.Edit == nil && src.Reason != "" {
		src.Reason = "the module call " + c.ModuleAddress + " sets it from its argument " + pathText(slices.Concat([]pathStep{{name: t.variable}}, t.sub)) + ": " + src.Reason
	}
	return src
}

// moduleManual is the source of a change whose value the module m sets
// in a way t says, that can't be traced back to the call.
func (e *driftEditor) moduleManual(c *DriftChange, m *driftModule, t traced) Source {
	src := Source{Range: t.rng, Kind: SourceModule}
	if t.file != "" {
		src.File = path.Join(m.dir, t.file)
	}
	switch {
	case t.literal && m.calls > 1:
		src.Reason = fmt.Sprintf("in the module call %s: the module %s sets it to a literal that its %d calls share: edit the module by hand, or make the value a variable of it for each call to set", c.ModuleAddress, m.dir, m.calls)
	case t.literal:
		src.Reason = fmt.Sprintf("in the module call %s: the module %s sets it to a literal: edit it there by hand", c.ModuleAddress, m.dir)
	default:
		src.Reason = fmt.Sprintf("in the module call %s: %s (%s): edit it by hand", c.ModuleAddress, t.reason, m.dir)
	}
	return src
}

// viaText says how the module m gets a value from its variable, as t
// traced it: the file, the line and the expression.
func (e *driftEditor) viaText(m *driftModule, t traced) string {
	text := path.Join(m.dir, t.file) + fmt.Sprintf(":%d", t.rng.Start.Line)
	if f := m.cfg.files[t.file]; f != nil && t.rng.End.Byte <= len(f.src) && t.rng.Start.Byte < t.rng.End.Byte {
		expr := string(f.src[t.rng.Start.Byte:t.rng.End.Byte])
		if !strings.Contains(expr, "\n") && len(expr) <= maxSourceExpression {
			text += " " + redactText(expr, e.forms)
		}
	}
	return text
}

// pathText writes steps as a path, such as tags or rule[0].status.
func pathText(steps []pathStep) string {
	var b strings.Builder
	for _, s := range steps {
		switch {
		case s.isIndex:
			fmt.Fprintf(&b, "[%d]", s.index)
		case b.Len() > 0 && !hclsyntax.ValidIdentifier(s.name):
			b.WriteString("[" + string(hclwrite.TokensForValue(cty.StringVal(s.name)).Bytes()) + "]")
		case b.Len() > 0:
			b.WriteString("." + s.name)
		default:
			b.WriteString(s.name)
		}
	}
	return b.String()
}

// inputText writes the input t traced to, such as var.policies["ReadOnly"].
func inputText(t traced) string {
	s := "var." + t.variable
	if sub := pathText(slices.Concat([]pathStep{{name: "x"}}, t.sub)); len(sub) > 1 {
		s += sub[1:]
	}
	return s
}

// instance follows the path into the argument of a call that makes
// several instances, for the instance key: into the call's for_each
// object literal, at the key, through each.value; or into a list or an
// object literal, at the call's count index or for_each key.
func (w *driftWalk) instance(block *hclsyntax.Block, key instanceKey) Source {
	name := w.steps[0].name
	attr, ok := block.Body.Attributes[name]
	if !ok {
		return w.manual(SourceModule, block.DefRange(), "the call makes several instances, and doesn't set "+name+" for them: edit it by hand")
	}
	w.attrAt = 1
	names := []string{name}
	switch x := attr.Expr.(type) {
	case *hclsyntax.ScopeTraversalExpr:
		item := forEachItem(block, key)
		if x.Traversal.RootName() != "each" || len(x.Traversal) < 2 || item == nil {
			break
		}
		if step, ok := x.Traversal[1].(hcl.TraverseAttr); !ok || step.Name != "value" {
			break
		}
		into, ok := traversalSteps(x.Traversal[2:])
		if !ok {
			break
		}
		w.steps = slices.Concat(w.steps[:1], into, w.steps[1:])
		w.mod.offset += len(into)
		w.mod.each = len(into)
		return w.expr(item.ValueExpr, holder{}, names, 1)
	case *hclsyntax.IndexExpr:
		if element := instanceElement(x, key); element != nil {
			return w.expr(element, holder{}, names, 1)
		}
	}
	return w.manual(SourceModule, attr.Expr.Range(), "the call makes several instances, and "+name+" isn't each one's own literal: edit it by hand")
}

// forEachItem returns the item of the object literal block's for_each is
// that key is the key of, or nil.
func forEachItem(block *hclsyntax.Block, key instanceKey) *hclsyntax.ObjectConsItem {
	attr, ok := block.Body.Attributes["for_each"]
	if !ok || !key.hasKey {
		return nil
	}
	obj, ok := attr.Expr.(*hclsyntax.ObjectConsExpr)
	if !ok {
		return nil
	}
	for i := range obj.Items {
		if keyName(obj.Items[i].KeyExpr) == key.key {
			return &obj.Items[i]
		}
	}
	return nil
}

// instanceElement returns, of x, a list literal indexed by count.index or
// an object literal by each.key, the element of the instance key key, or
// nil.
func instanceElement(x *hclsyntax.IndexExpr, key instanceKey) hclsyntax.Expression {
	index, ok := x.Key.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(index.Traversal) != 2 {
		return nil
	}
	step, ok := index.Traversal[1].(hcl.TraverseAttr)
	if !ok {
		return nil
	}
	switch c := x.Collection.(type) {
	case *hclsyntax.TupleConsExpr:
		if index.Traversal.RootName() == "count" && step.Name == "index" && key.isNum && key.index < len(c.Exprs) {
			return c.Exprs[key.index]
		}
	case *hclsyntax.ObjectConsExpr:
		if index.Traversal.RootName() != "each" || step.Name != "key" || !key.hasKey {
			return nil
		}
		for _, item := range c.Items {
			if keyName(item.KeyExpr) == key.key {
				return item.ValueExpr
			}
		}
	}
	return nil
}

// typeAt is driftWalk.typeAt for a call's argument: the module's
// variable's type, into the steps up to n, else the resource's attribute's
// type there.
func (cw *callWalk) typeAt(w *driftWalk, n int) cty.Type {
	if ty, ok := cw.module.variables[w.steps[0].name]; ok && n >= 1+cw.each {
		if t := descendType(ty, w.steps[1+cw.each:n]); t != cty.NilType && !t.HasDynamicTypes() {
			return t
		}
	}
	if n < cw.offset {
		return cty.NilType
	}
	return resourceTypeAt(w.e.schemas, w.c.Type, slices.Concat(cw.prefix, w.steps[cw.offset:n]))
}

// fits reports whether value, a map of the attributes a call's argument
// left out is to be set to, is all the module's variable needs: unless
// its type is an object with other attributes that aren't optional.
func (cw *callWalk) fits(variable string, value any) bool {
	ty, ok := cw.module.variables[variable]
	if !ok || !ty.IsObjectType() {
		return true
	}
	m, _ := value.(map[string]any)
	for name := range ty.AttributeTypes() {
		if _, set := m[name]; !set && !ty.AttributeOptional(name) {
			return false
		}
	}
	return true
}

// nullDefault reports whether the module's variable has null for its
// default, so that a call that leaves it out gets none, as one that sets
// it to null does. Where it has another default, or none, a note says the
// call sets it to null all the same; where the module's code doesn't
// say, there is no note.
func (cw *callWalk) nullDefault(variable string) (bool, string) {
	v := cw.module.cfg.variables[variable]
	if v == nil {
		return false, ""
	}
	attr, ok := v.Body.Attributes["default"]
	if !ok {
		return false, fmt.Sprintf("set to null, as the module %s has no default for var.%s", cw.module.dir, variable)
	}
	value, diags := attr.Expr.Value(nil)
	switch {
	case diags.HasErrors():
		return false, ""
	case value.IsNull():
		return true, ""
	}
	return false, fmt.Sprintf("set to null, as the default of var.%s in the module %s differs from the cloud's value", variable, cw.module.dir)
}

// resourceTypeAt returns the type of what steps go to in a resource of
// typ: through its nested blocks, to an argument, then into its value;
// cty.NilType if the schemas don't say.
func resourceTypeAt(schemas *tfjson.ProviderSchemas, typ string, steps []pathStep) cty.Type {
	var names []string
	for i, s := range steps {
		if s.isIndex {
			continue
		}
		names = append(names, s.name)
		if schemaBlockType(schemas, typ, names) {
			continue
		}
		return descendType(schemaType(schemas, "resource", typ, names), steps[i+1:])
	}
	return cty.NilType
}

// descendType returns the type of what steps go to in a value of ty, or
// cty.NilType if ty doesn't have it.
func descendType(ty cty.Type, steps []pathStep) cty.Type {
	for _, s := range steps {
		switch {
		case ty == cty.NilType:
			return ty
		case s.isIndex && (ty.IsListType() || ty.IsSetType()):
			ty = ty.ElementType()
		case s.isIndex && ty.IsTupleType() && s.index < len(ty.TupleElementTypes()):
			ty = ty.TupleElementTypes()[s.index]
		case !s.isIndex && ty.IsMapType():
			ty = ty.ElementType()
		case !s.isIndex && ty.IsObjectType() && ty.HasAttribute(s.name):
			ty = ty.AttributeType(s.name)
		default:
			return cty.NilType
		}
	}
	return ty
}
