// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"

	"github.com/IgnatG/infraharvest/internal/adapters"
)

// How a root's code sets an attribute that changed in the cloud
// (codeSource.Kind).
const (
	// sourceLiteral: a literal, or a key of an object or map literal.
	sourceLiteral = "literal"
	// sourceAbsent: no argument, as for a default or an empty value.
	sourceAbsent = "absent"
	// sourceLocal: a local value only this resource uses.
	sourceLocal = "local"
	// sourceSharedLocal: a local value other resources use too.
	sourceSharedLocal = "shared_local"
	// sourceReference: a reference to another resource or a data source.
	sourceReference = "reference"
	// sourceVariable: a variable, such as one whose default is another
	// root's value.
	sourceVariable = "variable"
	// sourceSensitiveVariable: a sensitive variable.
	sourceSensitiveVariable = "sensitive_variable"
	// sourceExpression: a function, a conditional, a for expression, a
	// dynamic block, or anything else that computes the value.
	sourceExpression = "expression"
	// sourceSharedTags: tags or labels several resources get from one
	// place: the provider's default tags, or a local their tags refer to.
	sourceSharedTags = "shared_tags"
	// sourceModule: the resource is in a module call, and what sets it
	// there can't be traced back to the call's arguments, or takes a
	// hand edit in the module.
	sourceModule = "module"
	// sourceResource: the whole resource, such as one deleted in the
	// cloud.
	sourceResource = "resource"
	// sourceNoBlock: the code has no block for the resource.
	sourceNoBlock = "no_block"
)

// codeSource is where a root's code sets an attribute that changed in the
// cloud, and how the code would take the cloud's value.
type codeSource struct {
	// File is the file, relative to the root; "" when there is none, as
	// for a resource in a module call.
	File string
	// Range is the expression that sets the attribute, or the block an
	// argument the code leaves out goes in.
	Range hcl.Range
	Kind  string
	// Edit takes the cloud's value into the code; nil when that is a hand
	// edit, as Reason says, or a secret.
	Edit *codeEdit
	// Also are the other edits Edit takes, made with it, or not at all:
	// for a literal of a generated module made a variable of it, the
	// module's and the call's (see promoteLiteral).
	Also []codeEdit
	// Reason says why there is no Edit.
	Reason string
	// instance says Edit is of the instance's own value, its key's in the
	// map the block's for_each is over (see eachResolver).
	instance bool
	// Secret says the value is a secret, which is never written.
	Secret bool
	// Module says, for a resource in a module call, how the module gets
	// the value from the argument of the call the edit is in, such as
	// ../../modules/bucket_0a1b2c3d/main.tf:4 var.tags.
	Module string
	// Note says what the report should say of an edit, such as a call's
	// argument set to null, whose module's default isn't null.
	Note string
}

// codeEdit replaces the bytes from Start to End of File, relative to
// the root, with Text.
type codeEdit struct {
	File       string
	Start, End int
	Text       []byte
	// Layout is the address of the resource block to lay out once edited,
	// as for an argument added at the end of it.
	Layout string
}

// appendEdit is the edit that adds text, whole lines, at the end of f,
// the file name, nil where there is none yet: on a line of its own, and
// after a blank line where blank says so and f has anything to keep
// apart from.
func appendEdit(f *hclFile, name string, text []byte, blank bool) codeEdit {
	var src []byte
	if f != nil {
		src = f.src
	}
	var before []byte
	if n := len(src); n > 0 {
		if src[n-1] != '\n' {
			before = append(before, '\n')
		}
		if blank {
			before = append(before, '\n')
		}
	}
	return codeEdit{File: name, Start: len(src), End: len(src), Text: slices.Concat(before, text)}
}

// refreshedResource is a resource as the refresh found it: the state's
// values, the cloud's, and which of the cloud's are sensitive.
type refreshedResource struct {
	before, after, afterSensitive any
}

// driftEditor finds where the code sets what changed in the cloud, and
// makes the edits that take the cloud's values.
type driftEditor struct {
	resources map[string]refreshedResource
	// schemas type the values written; nil writes them as their JSON has
	// them.
	schemas  *tfjson.ProviderSchemas
	defaults map[string][]string
	// refs are the managed resources' IDs, ARNs and other identifiers, to
	// refer to (see referenceIndex).
	refs map[string][]referenceTarget
	// sensitive are the values the plans mark sensitive, and forms the
	// forms text quotes them in: no edit may write one.
	sensitive, forms []string
	// secretForms are forms and the secrets a report hides among the
	// plans' other values (see driftSecretForms): no reason may quote one.
	secretForms []string
	// providerTagged are, by a resource's address and the path of a key
	// of its tags, why the resource can't leave out a key the provider's
	// default tags give it.
	providerTagged map[string]string
	// modules are the modules the root calls, by their keys in
	// modules.json (see loadDriftModules), to trace changes of their
	// resources to the calls.
	modules map[string]*driftModule
	// adapters are the run's (see DriftOptions.Adapters), whose Inverse
	// traces a change of a curated module's resource to its call (see
	// curatedAdapter).
	adapters []adapters.Adapter
	// env is, for a root environments share, the environment checked:
	// its code is every environment's, so only env's var file, envVars
	// (see envVarsPath), is edited, for a value a variable sets, or a
	// block only env has (see ownBlock).
	env, envVars string
	// ownFlags are, for a root environments share, the bool variables
	// only env sets true (see envOnlyFlags).
	ownFlags map[string]bool
	// current are the values the plan of the code has each resource of
	// the root module at, by address, which the refresh made the cloud's;
	// nil for one it creates.
	current map[string]any
}

func newDriftEditor(refresh, code *tfjson.Plan, defaults map[string][]string) *driftEditor {
	e := &driftEditor{resources: map[string]refreshedResource{}, defaults: defaults, providerTagged: map[string]string{}, current: map[string]any{}}
	if code != nil {
		for _, rc := range code.ResourceChanges {
			if rc.Mode == tfjson.ManagedResourceMode && rc.ModuleAddress == "" && rc.Change != nil {
				e.current[rc.Address] = rc.Change.Before
			}
		}
	}
	if refresh != nil {
		for _, rc := range refresh.ResourceDrift {
			if rc.Mode == tfjson.ManagedResourceMode && rc.Change != nil {
				e.resources[rc.Address] = refreshedResource{before: rc.Change.Before, after: rc.Change.After, afterSensitive: rc.Change.AfterSensitive}
			}
		}
	}
	var state *tfjson.State
	switch {
	case code != nil && code.PriorState != nil:
		state = code.PriorState
	case refresh != nil:
		state = refresh.PriorState
	}
	e.refs = referenceIndex(rootStateValues(state))
	seen := map[string]bool{}
	for _, p := range []*tfjson.Plan{refresh, code} {
		if p == nil {
			continue
		}
		for _, rcs := range [][]*tfjson.ResourceChange{p.ResourceDrift, p.ResourceChanges} {
			for _, rc := range rcs {
				if rc.Change != nil {
					collectSensitive(rc.Change.Before, rc.Change.BeforeSensitive, seen)
					collectSensitive(rc.Change.After, rc.Change.AfterSensitive, seen)
				}
			}
		}
	}
	e.sensitive = sortedKeys(seen)
	e.forms = textForms(e.sensitive)
	e.secretForms = driftSecretForms(nil, refresh, code)
	return e
}

// rootStateValues returns the attribute values of the managed resources
// of the root module of state, by address.
func rootStateValues(state *tfjson.State) map[string]map[string]any {
	values := map[string]map[string]any{}
	if state == nil || state.Values == nil || state.Values.RootModule == nil {
		return values
	}
	for _, r := range state.Values.RootModule.Resources {
		if r.Mode == tfjson.ManagedResourceMode {
			values[r.Address] = r.AttributeValues
		}
	}
	return values
}

// pathStep is a step of an attribute's path: a name, of an argument, a
// nested block or a map key, or an index.
type pathStep struct {
	name    string
	index   int
	isIndex bool
}

// splitPath splits a path such as root_block_device[0].volume_size or
// tags.kubernetes.io/role into its steps, with the keys of the maps in
// values, each a value the path goes into: a map key may have dots.
func splitPath(path string, values ...any) []pathStep {
	var steps []pathStep
	rest := path
	current := values
	for rest != "" {
		if rest[0] == '[' {
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return append(steps, pathStep{name: rest})
			}
			i, err := strconv.Atoi(rest[1:end])
			if err != nil {
				return append(steps, pathStep{name: rest})
			}
			steps = append(steps, pathStep{index: i, isIndex: true})
			rest = strings.TrimPrefix(rest[end+1:], ".")
			var next []any
			for _, v := range current {
				if l, ok := v.([]any); ok && i < len(l) {
					next = append(next, l[i])
				}
			}
			current = next
			continue
		}
		key := ""
		for _, v := range current {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			for k := range m {
				if len(k) > len(key) && (rest == k || strings.HasPrefix(rest, k+".") || strings.HasPrefix(rest, k+"[")) {
					key = k
				}
			}
		}
		if key == "" {
			key = rest
			if i := strings.IndexAny(rest, ".["); i > 0 {
				key = rest[:i]
			}
		}
		steps = append(steps, pathStep{name: key})
		rest = strings.TrimPrefix(rest[len(key):], ".")
		var next []any
		for _, v := range current {
			if m, ok := v.(map[string]any); ok {
				next = append(next, m[key])
			}
		}
		current = next
	}
	return steps
}

// valueAt returns the value at steps in v, and whether v has it.
func valueAt(v any, steps []pathStep) (any, bool) {
	for _, s := range steps {
		switch {
		case s.isIndex:
			l, ok := v.([]any)
			if !ok || s.index >= len(l) {
				return nil, false
			}
			v = l[s.index]
		default:
			m, ok := v.(map[string]any)
			if !ok {
				return nil, false
			}
			if v, ok = m[s.name]; !ok {
				return nil, false
			}
		}
	}
	return v, true
}

// resolve finds where cfg sets c's attribute, and the edit that takes
// the cloud's value (see codeSource). A secret, whether the plans mark it
// sensitive, a sensitive variable sets it, or the edit would write one,
// has no edit.
func (e *driftEditor) resolve(cfg *driftConfig, c *DriftChange) codeSource {
	src := e.locate(cfg, c)
	if reason, ok := e.providerTagged[c.Address+" "+c.Attribute]; ok && src.Edit == nil && c.value == nil && c.Kind == DriftChanged {
		src.Reason = reason
	}
	switch {
	case c.sensitive, src.Kind == sourceSensitiveVariable:
		src.Secret = true
	case src.Edit != nil && e.writesSecret(src.Edit.Text):
		src.Secret = true
	case src.Edit != nil && hasSensitive(maskKey(e.resources[c.Address].afterSensitive, topOf(c.Attribute))):
		// What is written may be more than the change, such as a whole
		// list: none of the argument may be sensitive.
		src.Secret = true
	}
	if src.Secret {
		src.Edit, src.Also = nil, nil
		if src.Kind != sourceSensitiveVariable {
			src.Reason = "a secret, which is never written into the code: move it into a sensitive variable by hand, and set that from your secret store"
		}
	}
	if e.env != "" && src.Edit != nil && src.Edit.File != e.envVars && !e.ownBlock(cfg, c, src) {
		src.Edit, src.Also = nil, nil
		src.Reason = fmt.Sprintf("the environments share this root's code, so it isn't edited for %s alone: edit it by hand, for every environment", e.env)
	}
	return src
}

// hasSensitive reports whether a mask, as a plan's after_sensitive has
// it, marks anything sensitive.
func hasSensitive(mask any) bool {
	switch m := mask.(type) {
	case bool:
		return m
	case map[string]any:
		for _, sub := range m {
			if hasSensitive(sub) {
				return true
			}
		}
	case []any:
		for _, sub := range m {
			if hasSensitive(sub) {
				return true
			}
		}
	}
	return false
}

// writesSecret reports whether text, a value to write, holds a value the
// plans mark sensitive, or a credential.
func (e *driftEditor) writesSecret(text []byte) bool {
	s := string(text)
	if credentialFormat(s) != "" || awsKeyPair(s) {
		return true
	}
	for _, form := range e.forms {
		if strings.Contains(s, form) {
			return true
		}
	}
	return false
}

func (e *driftEditor) locate(cfg *driftConfig, c *DriftChange) codeSource {
	if c.shared != nil {
		return e.locateShared(cfg, c)
	}
	if c.ModuleAddress != "" {
		return e.locateInModule(cfg, c)
	}
	address := trimInstanceKey(c.Address)
	b, ok := cfg.blocks[address]
	if !ok {
		return codeSource{Kind: sourceNoBlock, Reason: "the root's code has no block for it"}
	}
	w := &driftWalk{e: e, cfg: cfg, c: c, file: b.file, f: b.f, address: address, res: e.resources[c.Address]}
	if c.Kind == DriftDeleted {
		return w.manual(sourceResource, b.b.DefRange(), "deleted in the cloud: remove the resource from the code by hand, or apply to create it again")
	}
	if reason := w.instances(b.b); reason != "" {
		return w.manual(sourceExpression, b.b.DefRange(), reason)
	}
	w.steps = splitPath(c.Attribute, e.pathValues(c.Address)...)
	if len(w.steps) == 0 || w.steps[0].isIndex {
		return w.manual(sourceExpression, b.b.DefRange(), "edit it by hand")
	}
	src := w.body(b.b, nil, 0)
	if w.each != nil && src.Edit != nil && !src.instance {
		// What the block sets but through each.value, every instance gets.
		return codeSource{File: src.File, Range: src.Range, Kind: src.Kind, Reason: "every instance of the block's for_each gets it: edit it by hand, or give each key its own value in the map for_each is over"}
	}
	return src
}

// pathValues are the values the path of a change of the resource at
// address goes into, for splitPath: the state's, the cloud's, and the
// cloud's tags and labels with the provider's, as the change has them.
func (e *driftEditor) pathValues(address string) []any {
	res := e.resources[address]
	values := []any{res.before, res.after}
	if m, ok := res.after.(map[string]any); ok {
		for from, to := range foldedAttributes {
			if v, ok := m[from]; ok {
				values = append(values, map[string]any{to: v})
			}
		}
	}
	return values
}

// driftWalk follows a change's path through a resource's block.
type driftWalk struct {
	e       *driftEditor
	cfg     *driftConfig
	c       *DriftChange
	file    string // of the expression walked
	f       *hclFile
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
	if rng.End.Byte > len(w.f.src) || rng.Start.Byte > rng.End.Byte {
		return ""
	}
	return string(w.f.src[rng.Start.Byte:rng.End.Byte])
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
				if keyName(item.KeyExpr) == s.name {
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
				src.Edit = &codeEdit{File: w.file, Start: rng.Start.Byte, End: rng.End.Byte, Text: []byte("null")}
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
	src.Edit = &codeEdit{File: w.file, Start: rng.Start.Byte, End: rng.End.Byte, Text: text}
	return src
}

// isDefault reports whether text is the value the catalogue names as the
// default of the argument at path (see Options.Defaults).
func (w *driftWalk) isDefault(path []string, text []byte) bool {
	if w.mod != nil {
		// A call's argument: the default its adapter names, if curated.
		if w.mod.adapter == nil || len(path) != 1 {
			return false
		}
		tokens := parseExpr(string(text))
		if tokens == nil {
			return false
		}
		return w.mod.adapter.IsDefault(path[0], tokens)
	}
	want, ok := defaultValues(w.e.defaults[w.c.Type])[strings.Join(path, ".")]
	if !ok {
		return false
	}
	expr, diags := hclsyntax.ParseExpression(text, "", hcl.InitialPos)
	return !diags.HasErrors() && matchesValue(expr, want, nil)
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
			if keyName(obj.Items[j].KeyExpr) == s.name {
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
	return withoutNullAttributes(hclwrite.TokensForValue(v)).Bytes(), nil
}

// typeAt returns the type of the argument at path, then of what the
// steps sub go into in it, or cty.NilType if the schemas don't say.
func (w *driftWalk) typeAt(path []string, sub []pathStep) cty.Type {
	if w.mod != nil {
		return w.mod.typeAt(w, w.attrAt+len(sub))
	}
	return descendType(schemaType(w.e.schemas, "resource", w.c.Type, path), sub)
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

// insertArgument adds name = text at the end of block's body, which the
// block's layout then puts in its place (see layoutBody); nil if the
// block is on one line with something in it.
func (w *driftWalk) insertArgument(block *hclsyntax.Block, name string, text []byte) *codeEdit {
	src := w.f.src
	line := slices.Concat([]byte(name+" = "), text, []byte("\n"))
	opening, closing := block.OpenBraceRange, block.CloseBraceRange
	if opening.Start.Line == closing.Start.Line {
		if len(block.Body.Attributes)+len(block.Body.Blocks) > 0 {
			return nil
		}
		return &codeEdit{File: w.file, Start: opening.End.Byte, End: closing.Start.Byte, Text: slices.Concat([]byte("\n"), line), Layout: w.address}
	}
	at := lineStart(src, closing.Start.Byte)
	if len(bytes.TrimSpace(src[at:closing.Start.Byte])) > 0 {
		return nil
	}
	return &codeEdit{File: w.file, Start: at, End: at, Text: line, Layout: w.address}
}

// insertKey adds the key with the cloud's value to obj, an object
// literal.
func (w *driftWalk) insertKey(obj *hclsyntax.ObjectConsExpr, key string, path []string, i int) codeSource {
	if w.c.value == nil {
		return w.manual(sourceLiteral, obj.Range(), "the code doesn't have the key the cloud removed")
	}
	text, err := w.valueText(w.c.value, path, w.steps[min(w.attrAt, i):i])
	if err != nil {
		return w.unwritable(sourceLiteral, obj.Range())
	}
	item := keyText(key) + " = " + string(text)
	src := w.f.src
	rng := obj.Range()
	end := rng.End.Byte - 1 // the closing brace
	var edit *codeEdit
	switch {
	case obj.OpenRange.Start.Line == rng.End.Line && len(obj.Items) == 0:
		edit = &codeEdit{File: w.file, Start: rng.Start.Byte, End: rng.End.Byte, Text: []byte("{\n" + item + "\n}")}
	case obj.OpenRange.Start.Line == rng.End.Line:
		after := obj.Items[len(obj.Items)-1].ValueExpr.Range().End.Byte
		edit = &codeEdit{File: w.file, Start: after, End: after, Text: []byte(", " + item)}
	default:
		at := lineStart(src, end)
		if len(bytes.TrimSpace(src[at:end])) > 0 {
			return w.manual(sourceLiteral, rng, "add the key by hand")
		}
		edit = &codeEdit{File: w.file, Start: at, End: at, Text: []byte(item + "\n")}
	}
	return codeSource{File: w.file, Range: rng, Kind: sourceLiteral, Edit: edit}
}

// appendMergeArg adds { key = value } as the last argument of call, a
// merge, so that the resource's value wins.
func (w *driftWalk) appendMergeArg(call *hclsyntax.FunctionCallExpr, key string, path []string, i int) codeSource {
	if w.c.value == nil {
		return w.manual(sourceExpression, call.Range(), "merge() sets it, and the cloud removed the key: edit it by hand")
	}
	text, err := w.valueText(w.c.value, path, w.steps[min(w.attrAt, i):i])
	if err != nil {
		return w.unwritable(sourceExpression, call.Range())
	}
	at := call.CloseParenRange.Start.Byte
	return codeSource{File: w.file, Range: call.Range(), Kind: sourceLiteral, Edit: &codeEdit{File: w.file, Start: at, End: at, Text: []byte(", { " + keyText(key) + " = " + string(text) + " }")}}
}

// removeLines removes the lines of rng, if nothing else is on them.
func (w *driftWalk) removeLines(rng hcl.Range) *codeEdit {
	src := w.f.src
	start, end := lineStart(src, rng.Start.Byte), lineEnd(src, rng.End.Byte)
	if len(bytes.TrimSpace(src[start:rng.Start.Byte])) > 0 || len(bytes.TrimSpace(src[rng.End.Byte:end])) > 0 {
		return nil
	}
	return &codeEdit{File: w.file, Start: start, End: end}
}

// removeItem removes item from obj: its lines, if it is alone on them,
// else the item and the comma after it.
func (w *driftWalk) removeItem(obj *hclsyntax.ObjectConsExpr, item *hclsyntax.ObjectConsItem) *codeEdit {
	rng := hcl.RangeBetween(item.KeyExpr.Range(), item.ValueExpr.Range())
	if edit := w.removeLines(rng); edit != nil && obj.OpenRange.Start.Line != rng.Start.Line {
		return edit
	}
	src := w.f.src
	end := rng.End.Byte
	for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
		end++
	}
	if end < len(src) && src[end] == ',' {
		end++
		for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
			end++
		}
	}
	return &codeEdit{File: w.file, Start: rng.Start.Byte, End: end}
}

// apply makes in contents, files by name, the edits that take the
// cloud's values for changes, in order, each found again in the files as
// the edits before it left them; each edited file is laid out where an
// argument was added, and formatted, as terraform fmt would. A change's
// edits, its Edit and those Also has, are made together, or none of them.
// It returns the names of the files it edited, sorted, the sources it
// edited by, and the sources of the changes it couldn't edit: a hand
// edit, or a secret, which includes an edit that would add a value the
// literal secrets check takes for one (see literalSecrets).
func (e *driftEditor) apply(contents map[string][]byte, changes []*DriftChange) ([]string, map[*DriftChange]codeSource, map[*DriftChange]codeSource, error) {
	edited := map[string]bool{}
	applied := map[*DriftChange]codeSource{}
	failed := map[*DriftChange]codeSource{}
	var cfg *driftConfig
	for _, c := range changes {
		// Only the files the change before edited are parsed again.
		e.syncModules(contents)
		var err error
		if cfg, err = cfg.reparse(contents); err != nil {
			return nil, nil, nil, err
		}
		src := e.resolve(cfg, c)
		if src.Edit == nil {
			failed[c] = src
			continue
		}
		next, reason, secret := editFiles(contents, slices.Concat([]codeEdit{*src.Edit}, src.Also))
		if reason != "" {
			src.Edit, src.Also, src.Reason, src.Secret = nil, nil, reason, secret
			failed[c] = src
			continue
		}
		for name, content := range next {
			contents[name] = content
			edited[name] = true
		}
		applied[c] = src
	}
	return sortedKeys(edited), applied, failed, nil
}

// editFiles returns the files of contents edits change, as they edit
// them, each laid out where an argument was added and formatted; or why
// they can't be made, as an edited file wouldn't parse or would hold a
// literal secret it didn't (secret).
func editFiles(contents map[string][]byte, edits []codeEdit) (map[string][]byte, string, bool) {
	byFile := map[string][]codeEdit{}
	for _, edit := range edits {
		byFile[edit.File] = append(byFile[edit.File], edit)
	}
	next := map[string][]byte{}
	for _, name := range sortedKeys(byFile) {
		old := contents[name]
		fileEdits := byFile[name]
		// From the last, so that the offsets of the others hold.
		sort.SliceStable(fileEdits, func(i, j int) bool { return fileEdits[i].Start > fileEdits[j].Start })
		content := old
		var layouts []string
		for _, edit := range fileEdits {
			content = slices.Concat(content[:edit.Start], edit.Text, content[edit.End:])
			if edit.Layout != "" {
				layouts = append(layouts, edit.Layout)
			}
		}
		for _, address := range layouts {
			content = layoutResource(content, address)
		}
		content = hclwrite.Format(content)
		edited, err := parseHCL(name, content)
		if err != nil {
			return nil, "the edit wouldn't parse: edit it by hand", false
		}
		// The findings, not their count: an edit may remove one secret and
		// add another.
		var had Check
		if f, err := parseHCL(name, old); err == nil {
			had.Details = literalSecrets(f)
		}
		if len(addedDetails(had, Check{Details: literalSecrets(edited)})) > 0 {
			return nil, "a secret, which is never written into the code: move it into a sensitive variable by hand, and set that from your secret store", true
		}
		next[name] = content
	}
	return next, "", false
}

// layoutResource lays out the block of the resource at address in src
// (see layoutBody), and only that block.
func layoutResource(src []byte, address string) []byte {
	f, err := parseHCL("", src)
	if err != nil {
		return src
	}
	for _, b := range f.syntax.Blocks {
		if b.Type != "resource" || len(b.Labels) != 2 || b.Labels[0]+"."+b.Labels[1] != address || b.OpenBraceRange.Start.Line == b.CloseBraceRange.Start.Line {
			continue
		}
		inner, ok := layoutBody(src, b, true)
		if !ok {
			return src
		}
		return slices.Concat(src[:b.OpenBraceRange.End.Byte], []byte("\n"), inner, src[b.CloseBraceRange.Start.Byte:])
	}
	return src
}

// reportSource is src as a report shows it: the file and line, the
// expression if on one line, with secrets left out, and how a module gets
// the value from the call; nil without a file.
func reportSource(cfg *driftConfig, src codeSource, forms []string) *DriftSource {
	if src.File == "" {
		return nil
	}
	out := &DriftSource{File: src.File, Line: src.Range.Start.Line, ModulePath: src.Module}
	f := cfg.files[src.File]
	if f == nil || src.Kind == sourceAbsent || src.Kind == sourceResource || src.Kind == sourceModule {
		return out
	}
	rng := src.Range
	if rng.End.Byte > len(f.src) || rng.Start.Byte >= rng.End.Byte {
		return out
	}
	expr := string(f.src[rng.Start.Byte:rng.End.Byte])
	if !strings.Contains(expr, "\n") && len(expr) <= maxSourceExpression && credentialFormat(expr) == "" {
		out.Expression = redactText(expr, forms)
	}
	return out
}
