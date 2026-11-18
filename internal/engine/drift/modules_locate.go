// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// locateInModule finds where the root's code sets c's attribute of a
// resource in a module call, and the edit that takes the cloud's value:
// in the argument of the root's call that the module sets it from, as
// its code says (see moduleTracer), followed through the calls of other
// modules inside it, or, in a curated module, as its adapter's Inverse
// says; then as for a resource's argument (see driftWalk) in that
// argument. Nothing under the modules is edited, but a literal of a
// generated module that calls or environments share (see promoteLiteral).
func (e *driftEditor) locateInModule(cfg *driftConfig, c *Change) codeSource {
	manual := func(reason string) codeSource {
		return codeSource{Kind: sourceModule, Reason: "in the module call " + c.ModuleAddress + ": " + reason}
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
			reason := "modules.json doesn't list the module, so what it sets can't be told: change the call by hand"
			if why := e.unreadModule(moduleKey(calls[:i+1])); why != "" {
				reason = why + ", so what it sets can't be told: change the call by hand"
			}
			src := manual(reason)
			src.File, src.Range = blocks[0].file, blocks[0].b.DefRange()
			return src
		}
		parent = mods[i].cfg
	}
	callSource := func(reason string) codeSource {
		src := manual(reason)
		src.File, src.Range = blocks[0].file, blocks[0].b.DefRange()
		return src
	}
	if c.Kind == Deleted {
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
		if adapter = curatedAdapter(blocks[i].b, e.adapters); adapter != nil {
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
		tr := &moduleTracer{m: m, steps: steps, block: b.b, key: key}
		if t = tr.body(b.file, b.b, 0); t.variable == "" {
			if t.literal && len(calls) == 1 {
				if src, ok := e.promoteLiteral(cfg, c, blocks[0], calls[0].name, m, b, t, steps); ok {
					return src
				}
			}
			return e.moduleManual(c, m, t)
		}
		via = append(via, e.viaText(m, t))
	}
	// Up through the calls of modules inside modules, to the root's, each
	// at its instance's key.
	rest := steps[t.at:]
	for i := level; i > 0; i-- {
		callSteps := slices.Concat([]pathStep{{name: t.variable}}, t.sub, rest)
		tr := &moduleTracer{m: mods[i-1], steps: callSteps, block: blocks[i].b, key: calls[i].key}
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
	var src codeSource
	if hasInstances(b.b) {
		src = w.instance(b.b, calls[0].key)
	} else {
		src = w.body(b.b, nil, 0)
	}
	src.Module = strings.Join(via, "; ")
	if src.Edit == nil && src.Reason != "" {
		src.Reason = "the module call " + c.ModuleAddress + " sets it from its argument " + pathText(slices.Concat([]pathStep{{name: t.variable}}, t.sub)) + ": " + src.Reason
		if other := otherArgumentWithKey(blocks[0].b, t.variable, steps, c); adapter != nil && other != "" && strings.Contains(src.Reason, "doesn't have the key") {
			src.Reason += "; the call's argument " + other + " has the key, which the module may give this resource from there and others too: remove it from there if no resource should have it, else edit it by hand"
		}
	}
	return src
}

// moduleManual is the source of a change whose value the module m sets
// in a way t says, that can't be traced back to the call.
func (e *driftEditor) moduleManual(c *Change, m *driftModule, t traced) codeSource {
	src := codeSource{Range: t.rng, Kind: sourceModule}
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
	if f := m.cfg.files[t.file]; f != nil && t.rng.End.Byte <= len(f.Src) && t.rng.Start.Byte < t.rng.End.Byte {
		expr := string(f.Src[t.rng.Start.Byte:t.rng.End.Byte])
		if !strings.Contains(expr, "\n") && len(expr) <= maxSourceExpression {
			text += " " + secret.RedactText(expr, e.forms)
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
// several instances, for the instance key: through each.value, into the
// item at the key of the call's for_each; or into the element at the
// call's count index or for_each key of what the argument indexes, such
// as local.queues[count.index].settings. What it goes into is a list or
// an object literal (see callLiteral).
func (w *driftWalk) instance(block *hclsyntax.Block, key instanceKey) codeSource {
	name := w.steps[0].name
	attr, ok := block.Body.Attributes[name]
	if !ok {
		return w.manual(sourceModule, block.DefRange(), "the call makes several instances, and doesn't set "+name+" for them: edit it by hand")
	}
	w.attrAt = 1
	lit, ok := w.instanceValue(block, attr.Expr, key)
	if !ok {
		return w.manual(sourceModule, attr.Expr.Range(), "the call makes several instances, and "+name+" isn't each one's own literal: edit it by hand")
	}
	w.file, w.f = lit.file, lit.f
	w.steps = slices.Concat(w.steps[:1], lit.into, w.steps[1:])
	w.mod.offset += len(lit.into)
	w.mod.each = len(lit.into)
	return w.expr(lit.expr, holder{}, []string{name}, 1)
}

// instanceLiteral is a literal of a call's argument: expr, in file, and
// the steps into it that the argument's value is at.
type instanceLiteral struct {
	expr hclsyntax.Expression
	into []pathStep
	file string
	f    *hclfile.File
}

// instanceValue returns where e, the argument of block, a call that makes
// several instances, has the instance key's own value (see instance).
func (w *driftWalk) instanceValue(block *hclsyntax.Block, e hclsyntax.Expression, key instanceKey) (instanceLiteral, bool) {
	switch x := e.(type) {
	case *hclsyntax.ParenthesesExpr:
		return w.instanceValue(block, x.Expression, key)
	case *hclsyntax.ScopeTraversalExpr:
		forEach, set := block.Body.Attributes["for_each"]
		if x.Traversal.RootName() != "each" || len(x.Traversal) < 2 || !set || !key.hasKey {
			return instanceLiteral{}, false
		}
		if step, ok := x.Traversal[1].(hcl.TraverseAttr); !ok || step.Name != "value" {
			return instanceLiteral{}, false
		}
		into, ok := traversalSteps(x.Traversal[2:])
		if !ok {
			return instanceLiteral{}, false
		}
		lit, ok := w.callLiteral(block, forEach.Expr)
		obj, isObj := lit.expr.(*hclsyntax.ObjectConsExpr)
		if !ok || !isObj {
			return instanceLiteral{}, false
		}
		lit.expr, lit.into = objectItem(obj, key.key), into
		return lit, lit.expr != nil
	case *hclsyntax.IndexExpr:
		lit, ok := w.callLiteral(block, x.Collection)
		if !ok {
			return instanceLiteral{}, false
		}
		lit.expr = instanceElement(lit.expr, x.Key, key)
		return lit, lit.expr != nil
	case *hclsyntax.RelativeTraversalExpr:
		lit, ok := w.instanceValue(block, x.Source, key)
		into, valid := traversalSteps(x.Traversal)
		if !ok || !valid {
			return instanceLiteral{}, false
		}
		lit.into = slices.Concat(lit.into, into)
		return lit, true
	}
	return instanceLiteral{}, false
}

// callLiteral returns the list or object literal e, an expression of the
// call block, is: written in the call; the value of a local; or, in a
// root environments share, the value the environment's var file gives a
// variable. A local or a variable is followed only where nothing outside
// the call refers to it, as an edit of it reaches only the call then.
func (w *driftWalk) callLiteral(block *hclsyntax.Block, e hclsyntax.Expression) (instanceLiteral, bool) {
	lit := instanceLiteral{expr: e, file: w.file, f: w.f}
	if x, ok := e.(*hclsyntax.ScopeTraversalExpr); ok && len(x.Traversal) == 2 {
		step, _ := x.Traversal[1].(hcl.TraverseAttr)
		root, name := x.Traversal.RootName(), step.Name
		switch {
		case name == "":
		case root == "local":
			def, ok := w.cfg.locals[name]
			if !ok || w.cfg.uses[name] > blockUses(block, root, name) {
				return instanceLiteral{}, false
			}
			lit = instanceLiteral{expr: def.attr.Expr, file: def.file, f: def.f}
		case root == "var" && w.e.env != "":
			v := w.cfg.variables[name]
			val, ok := w.envValue(name)
			if v == nil || sensitiveVariable(v) || !ok || w.cfg.varUses[name] > blockUses(block, root, name) {
				return instanceLiteral{}, false
			}
			lit = instanceLiteral{expr: val.Attr.Expr, file: val.Path, f: val.File}
		}
	}
	switch lit.expr.(type) {
	case *hclsyntax.ObjectConsExpr, *hclsyntax.TupleConsExpr:
		return lit, true
	}
	return instanceLiteral{}, false
}

// blockUses counts the references in block to root.name, such as
// local.queues.
func blockUses(block *hclsyntax.Block, root, name string) int {
	n := 0
	for _, t := range hclfile.Traversals(block.Body) {
		if t.RootName() != root || len(t) < 2 {
			continue
		}
		if step, ok := t[1].(hcl.TraverseAttr); ok && step.Name == name {
			n++
		}
	}
	return n
}

// objectItem returns the value of obj's item at key, or nil.
func objectItem(obj *hclsyntax.ObjectConsExpr, key string) hclsyntax.Expression {
	for _, item := range obj.Items {
		if hclfile.KeyName(item.KeyExpr) == key {
			return item.ValueExpr
		}
	}
	return nil
}

// instanceElement returns, of collection, a list literal index is
// count.index into or an object literal it is each.key into, the element
// of the instance key key, or nil.
func instanceElement(collection, index hclsyntax.Expression, key instanceKey) hclsyntax.Expression {
	ref, ok := index.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(ref.Traversal) != 2 {
		return nil
	}
	step, ok := ref.Traversal[1].(hcl.TraverseAttr)
	if !ok {
		return nil
	}
	switch c := collection.(type) {
	case *hclsyntax.TupleConsExpr:
		if ref.Traversal.RootName() == "count" && step.Name == "index" && key.isNum && key.index < len(c.Exprs) {
			return c.Exprs[key.index]
		}
	case *hclsyntax.ObjectConsExpr:
		if ref.Traversal.RootName() == "each" && step.Name == "key" && key.hasKey {
			return objectItem(c, key.key)
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
		return descendType(tfrun.AttributeType(schemas, typ, names), steps[i+1:])
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

// otherArgumentWithKey returns the name of an argument of the call block,
// but for skip, that is an object literal with the key c's attribute ends
// in, where c is a key removed in the cloud; "" if there is none.
func otherArgumentWithKey(block *hclsyntax.Block, skip string, steps []pathStep, c *Change) string {
	if c.value != nil || c.Kind != Changed || len(steps) < 2 || steps[len(steps)-1].isIndex {
		return ""
	}
	key := steps[len(steps)-1].name
	for _, name := range slices.Sorted(maps.Keys(block.Body.Attributes)) {
		obj, ok := block.Body.Attributes[name].Expr.(*hclsyntax.ObjectConsExpr)
		if !ok || name == skip {
			continue
		}
		for _, item := range obj.Items {
			if hclfile.KeyName(item.KeyExpr) == key {
				return name
			}
		}
	}
	return ""
}
