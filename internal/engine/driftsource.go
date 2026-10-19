// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// How a root's code sets an attribute that changed in the cloud
// (Source.Kind).
const (
	// SourceLiteral: a literal, or a key of an object or map literal.
	SourceLiteral = "literal"
	// SourceAbsent: no argument, as for a default or an empty value.
	SourceAbsent = "absent"
	// SourceLocal: a local value only this resource uses.
	SourceLocal = "local"
	// SourceSharedLocal: a local value other resources use too.
	SourceSharedLocal = "shared_local"
	// SourceReference: a reference to another resource or a data source.
	SourceReference = "reference"
	// SourceVariable: a variable, such as one whose default is another
	// root's value.
	SourceVariable = "variable"
	// SourceSensitiveVariable: a sensitive variable.
	SourceSensitiveVariable = "sensitive_variable"
	// SourceExpression: a function, a conditional, a for expression, a
	// dynamic block, or anything else that computes the value.
	SourceExpression = "expression"
	// SourceSharedTags: tags or labels several resources get from one
	// place: the provider's default tags, or a local their tags refer to.
	SourceSharedTags = "shared_tags"
	// SourceModule: the resource is in a module call.
	SourceModule = "module"
	// SourceResource: the whole resource, such as one deleted in the
	// cloud.
	SourceResource = "resource"
	// SourceNoBlock: the code has no block for the resource.
	SourceNoBlock = "no_block"
)

// Source is where a root's code sets an attribute that changed in the
// cloud, and how the code would take the cloud's value.
type Source struct {
	// File is the file, relative to the root; "" when there is none, as
	// for a resource in a module call.
	File string
	// Range is the expression that sets the attribute, or the block an
	// argument the code leaves out goes in.
	Range hcl.Range
	Kind  string
	// Edit takes the cloud's value into the code; nil when that is a hand
	// edit, as Reason says, or a secret.
	Edit *SourceEdit
	// Reason says why there is no Edit.
	Reason string
	// Secret says the value is a secret, which is never written.
	Secret bool
}

// SourceEdit replaces the bytes from Start to End of File, relative to
// the root, with Text.
type SourceEdit struct {
	File       string
	Start, End int
	Text       []byte
	// Layout is the address of the resource block to lay out once edited,
	// as for an argument added at the end of it.
	Layout string
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
	// providerTagged are, by a resource's address and the path of a key
	// of its tags, why the resource can't leave out a key the provider's
	// default tags give it.
	providerTagged map[string]string
}

func newDriftEditor(refresh, code *tfjson.Plan, defaults map[string][]string) *driftEditor {
	e := &driftEditor{resources: map[string]refreshedResource{}, defaults: defaults, providerTagged: map[string]string{}}
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

// driftConfig is a root's configuration files, parsed, as resolve reads
// them.
type driftConfig struct {
	files     map[string]*hclFile // by name, relative to the root
	blocks    map[string]driftBlock
	locals    map[string]driftLocal
	uses      map[string]int // how often each local is referred to
	variables map[string]*hclsyntax.Block
}

type driftBlock struct {
	file string
	f    *hclFile
	b    *hclsyntax.Block
}

type driftLocal struct {
	file string
	f    *hclFile
	attr *hclsyntax.Attribute
}

// tfContents returns the contents of the .tf files of dir, by name,
// without override files.
func tfContents(dir string) (map[string][]byte, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		return nil, err
	}
	contents := map[string][]byte{}
	for _, path := range paths {
		name := filepath.Base(path)
		if name == "override.tf" || strings.HasSuffix(name, "_override.tf") {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		contents[name] = content
	}
	return contents, nil
}

func parseDriftConfig(contents map[string][]byte) (*driftConfig, error) {
	cfg := &driftConfig{files: map[string]*hclFile{}, blocks: map[string]driftBlock{}, locals: map[string]driftLocal{}, uses: map[string]int{}, variables: map[string]*hclsyntax.Block{}}
	for _, name := range sortedKeys(contents) {
		f, err := parseHCL(name, contents[name])
		if err != nil {
			return nil, err
		}
		cfg.files[name] = f
		for _, b := range f.syntax.Blocks {
			switch {
			case b.Type == "resource" && len(b.Labels) == 2:
				cfg.blocks[b.Labels[0]+"."+b.Labels[1]] = driftBlock{file: name, f: f, b: b}
			case b.Type == "locals":
				for n, attr := range b.Body.Attributes {
					cfg.locals[n] = driftLocal{file: name, f: f, attr: attr}
				}
			case b.Type == "variable" && len(b.Labels) == 1:
				cfg.variables[b.Labels[0]] = b
			}
		}
		for _, t := range allTraversals(f.syntax) {
			if t.RootName() != "local" || len(t) < 2 {
				continue
			}
			if step, ok := t[1].(hcl.TraverseAttr); ok {
				cfg.uses[step.Name]++
			}
		}
	}
	return cfg, nil
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
// the cloud's value (see Source). A secret, whether the plans mark it
// sensitive, a sensitive variable sets it, or the edit would write one,
// has no edit.
func (e *driftEditor) resolve(cfg *driftConfig, c *DriftChange) Source {
	src := e.locate(cfg, c)
	if reason, ok := e.providerTagged[c.Address+" "+c.Attribute]; ok && src.Edit == nil && c.value == nil && c.Kind == DriftChanged {
		src.Reason = reason
	}
	switch {
	case c.sensitive, src.Kind == SourceSensitiveVariable:
		src.Secret = true
	case src.Edit != nil && e.writesSecret(src.Edit.Text):
		src.Secret = true
	case src.Edit != nil && hasSensitive(maskKey(e.resources[c.Address].afterSensitive, topOf(c.Attribute))):
		// What is written may be more than the change, such as a whole
		// list: none of the argument may be sensitive.
		src.Secret = true
	}
	if src.Secret {
		src.Edit = nil
		if src.Kind != SourceSensitiveVariable {
			src.Reason = "a secret, which is never written into the code: move it into a sensitive variable by hand, and set that from your secret store"
		}
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

func (e *driftEditor) locate(cfg *driftConfig, c *DriftChange) Source {
	if c.shared != nil {
		return e.locateShared(cfg, c)
	}
	if c.ModuleAddress != "" {
		return Source{Kind: SourceModule, Reason: "in the module call " + c.ModuleAddress + ": changing the call to match is a hand edit for now"}
	}
	address := trimInstanceKey(c.Address)
	b, ok := cfg.blocks[address]
	if !ok {
		return Source{Kind: SourceNoBlock, Reason: "the root's code has no block for it"}
	}
	w := &driftWalk{e: e, cfg: cfg, c: c, file: b.file, f: b.f, address: address, res: e.resources[c.Address]}
	if c.Kind == DriftDeleted {
		return w.manual(SourceResource, b.b.DefRange(), "deleted in the cloud: remove the resource from the code by hand, or apply to create it again")
	}
	for _, meta := range []string{"count", "for_each"} {
		if _, ok := b.b.Body.Attributes[meta]; ok {
			return w.manual(SourceExpression, b.b.DefRange(), "the block makes several resources with "+meta+": edit it by hand")
		}
	}
	values := []any{w.res.before, w.res.after}
	if m, ok := w.res.after.(map[string]any); ok {
		for from, to := range foldedAttributes {
			if v, ok := m[from]; ok {
				values = append(values, map[string]any{to: v})
			}
		}
	}
	w.steps = splitPath(c.Attribute, values...)
	if len(w.steps) == 0 || w.steps[0].isIndex {
		return w.manual(SourceExpression, b.b.DefRange(), "edit it by hand")
	}
	return w.body(b.b, nil, 0)
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
}

// holder is what holds an expression: an argument of a block, an item of
// an object, or neither, which can't be removed.
type holder struct {
	attr *hclsyntax.Attribute
	obj  *hclsyntax.ObjectConsExpr
	item *hclsyntax.ObjectConsItem
}

func (w *driftWalk) manual(kind string, rng hcl.Range, reason string) Source {
	return Source{File: w.file, Range: rng, Kind: kind, Reason: reason}
}

func (w *driftWalk) text(rng hcl.Range) string {
	if rng.End.Byte > len(w.f.src) || rng.Start.Byte > rng.End.Byte {
		return ""
	}
	return string(w.f.src[rng.Start.Byte:rng.End.Byte])
}

// at returns the cloud's value at the first n steps, or nil if the cloud
// has none; at the whole path, the change's own value.
func (w *driftWalk) at(n int) any {
	if n >= len(w.steps) {
		return w.c.value
	}
	if v, ok := valueAt(w.res.after, w.steps[:n]); ok {
		return v
	}
	if m, ok := w.res.after.(map[string]any); ok {
		for from, to := range foldedAttributes {
			if to == w.steps[0].name {
				if v, ok := valueAt(map[string]any{to: m[from]}, w.steps[:n]); ok {
					return v
				}
			}
		}
	}
	return nil
}

// body follows the path from step i in block's body; names are the block
// types from the resource's block to block, for the schema.
func (w *driftWalk) body(block *hclsyntax.Block, names []string, i int) Source {
	s := w.steps[i]
	if s.isIndex {
		return w.manual(SourceExpression, block.DefRange(), "edit it by hand")
	}
	path := append(slices.Clone(names), s.name)
	if attr, ok := block.Body.Attributes[s.name]; ok {
		w.attrAt = i + 1
		return w.expr(attr.Expr, holder{attr: attr}, path, i+1)
	}
	var nested []*hclsyntax.Block
	for _, nb := range block.Body.Blocks {
		if nb.Type == "dynamic" && len(nb.Labels) == 1 && nb.Labels[0] == s.name {
			return w.manual(SourceExpression, nb.DefRange(), "a dynamic "+s.name+" block makes it: edit it by hand")
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
			return w.manual(SourceExpression, nested[len(nested)-1].DefRange(), "a "+s.name+" block was added or removed in the cloud: edit the blocks by hand")
		}
		return w.body(nested[j], path, next)
	}
	if w.e.schemas != nil && schemaBlockType(w.e.schemas, w.c.Type, path) {
		return w.manual(SourceAbsent, block.DefRange(), "the code has no "+s.name+" block: add it by hand")
	}
	// An argument the code leaves out.
	w.attrAt = i + 1
	var value any
	var sub []pathStep
	if i+2 == len(w.steps) && !w.steps[i+1].isIndex {
		// A key of a map, alone: the other keys the cloud has are other
		// changes, each decided on its own.
		if w.c.value != nil {
			value = map[string]any{w.steps[i+1].name: w.c.value}
		}
	} else {
		value = w.at(i + 1)
	}
	if value == nil {
		return w.manual(SourceAbsent, block.DefRange(), "the code doesn't set it, and the cloud has no value for it")
	}
	text, err := w.valueText(value, path, sub)
	if err != nil {
		return w.manual(SourceAbsent, block.DefRange(), "the cloud's value can't be written: add it by hand")
	}
	edit := w.insertArgument(block, s.name, text)
	if edit == nil {
		return w.manual(SourceAbsent, block.DefRange(), "the block is on one line: add the argument by hand")
	}
	return Source{File: w.file, Range: block.DefRange(), Kind: SourceAbsent, Edit: edit}
}

// expr follows the path from step i in e, which h holds; path names the
// argument e is, or is in, for the schema.
func (w *driftWalk) expr(e hclsyntax.Expression, h holder, path []string, i int) Source {
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
			return w.manual(SourceExpression, x.Range(), "an expression computes it: edit it by hand")
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
		return w.manual(SourceExpression, x.Range(), "an expression computes it: edit it by hand")
	case *hclsyntax.TupleConsExpr:
		// A list is written whole: the order of a set's elements is the
		// provider's, so its elements can't be told apart by index.
		if literalExpr(x) {
			return w.replace(x, h, path, i)
		}
		if i < len(w.steps) && w.steps[i].isIndex && w.steps[i].index < len(x.Exprs) {
			return w.expr(x.Exprs[w.steps[i].index], holder{}, path, i+1)
		}
		return w.manual(SourceExpression, x.Range(), "an expression computes it: edit it by hand")
	case *hclsyntax.FunctionCallExpr:
		if x.Name == "merge" && i+1 == len(w.steps) && !w.steps[i].isIndex && len(x.Args) > 0 && !x.ExpandFinal {
			// The resource's own keys, which merge puts over the others.
			if last, ok := x.Args[len(x.Args)-1].(*hclsyntax.ObjectConsExpr); ok {
				return w.expr(last, holder{}, path, i)
			}
			return w.appendMergeArg(x, w.steps[i].name, path, i+1)
		}
		return w.manual(SourceExpression, x.Range(), x.Name+"() computes it: edit it by hand")
	case *hclsyntax.TemplateExpr:
		if !x.IsStringLiteral() {
			return w.manual(SourceExpression, x.Range(), "a template computes it: edit it by hand")
		}
		return w.replace(x, h, path, i)
	case *hclsyntax.LiteralValueExpr:
		return w.replace(x, h, path, i)
	}
	return w.manual(SourceExpression, e.Range(), "an expression computes it: edit it by hand")
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
func (w *driftWalk) replace(x hclsyntax.Expression, h holder, path []string, i int) Source {
	rng := x.Range()
	value := w.at(i)
	src := Source{File: w.file, Range: rng, Kind: SourceLiteral}
	if value == nil {
		switch {
		case h.attr != nil:
			src.Edit = w.removeLines(h.attr.SrcRange)
		case h.item != nil:
			src.Edit = w.removeItem(h.obj, h.item)
		}
		if src.Edit == nil {
			return w.manual(SourceLiteral, rng, "the cloud has no value for it: edit it by hand")
		}
		return src
	}
	text, err := w.valueText(value, path, w.steps[min(w.attrAt, i):i])
	if err != nil {
		return w.manual(SourceLiteral, rng, "the cloud's value can't be written: edit it by hand")
	}
	if h.attr != nil && i == w.attrAt && w.isDefault(path, text) {
		if edit := w.removeLines(h.attr.SrcRange); edit != nil {
			src.Edit = edit
			return src
		}
	}
	src.Edit = &SourceEdit{File: w.file, Start: rng.Start.Byte, End: rng.End.Byte, Text: text}
	return src
}

// isDefault reports whether text is the value the catalogue names as the
// default of the argument at path (see Options.Defaults).
func (w *driftWalk) isDefault(path []string, text []byte) bool {
	want, ok := defaultValues(w.e.defaults[w.c.Type])[strings.Join(path, ".")]
	if !ok {
		return false
	}
	expr, diags := hclsyntax.ParseExpression(text, "", hcl.InitialPos)
	return !diags.HasErrors() && matchesValue(expr, want, nil)
}

func (w *driftWalk) traversal(x *hclsyntax.ScopeTraversalExpr, path []string, i int) Source {
	name := ""
	if len(x.Traversal) == 2 {
		if step, ok := x.Traversal[1].(hcl.TraverseAttr); ok {
			name = step.Name
		}
	}
	switch x.Traversal.RootName() {
	case "local":
		if name == "" {
			return w.manual(SourceExpression, x.Range(), "an expression computes it: edit it by hand")
		}
		return w.local(x, name, path, i)
	case "var":
		if name == "" {
			return w.manual(SourceExpression, x.Range(), "an expression computes it: edit it by hand")
		}
		return w.variable(x, name)
	case "each", "count", "path", "terraform", "self":
		return w.manual(SourceExpression, x.Range(), "an expression computes it: edit it by hand")
	}
	return w.reference(x, path, i)
}

// maxLocalDepth bounds how many locals referring to locals resolve
// follows.
const maxLocalDepth = 4

// local follows the path into local.name, which x refers to: into its
// value if this resource alone uses it; else the resource gets its own
// value, merged over the local's for a key of it.
func (w *driftWalk) local(x *hclsyntax.ScopeTraversalExpr, name string, path []string, i int) Source {
	ref := w.text(x.Range())
	def, ok := w.cfg.locals[name]
	if !ok || w.depth >= maxLocalDepth {
		return w.manual(SourceLocal, x.Range(), ref+" sets it: edit it by hand")
	}
	if w.cfg.uses[name] <= 1 {
		inner := *w
		inner.file, inner.f, inner.depth = def.file, def.f, w.depth+1
		src := inner.expr(def.attr.Expr, holder{}, path, i)
		if src.Kind == SourceLiteral || src.Kind == SourceAbsent {
			src.Kind = SourceLocal
		}
		return src
	}
	if i+1 == len(w.steps) && !w.steps[i].isIndex {
		if w.c.value == nil {
			return w.manual(SourceSharedLocal, x.Range(), ref+", which other resources use too, has the key the cloud removed: edit it by hand")
		}
		text, err := w.valueText(w.c.value, path, w.steps[min(w.attrAt, i+1):i+1])
		if err != nil {
			return w.manual(SourceSharedLocal, x.Range(), "the cloud's value can't be written: edit it by hand")
		}
		merged := fmt.Sprintf("merge(%s, { %s = %s })", ref, keyText(w.steps[i].name), text)
		return Source{File: w.file, Range: x.Range(), Kind: SourceSharedLocal, Edit: &SourceEdit{File: w.file, Start: x.Range().Start.Byte, End: x.Range().End.Byte, Text: []byte(merged)}}
	}
	value := w.at(i)
	if value == nil {
		return w.manual(SourceSharedLocal, x.Range(), ref+", which other resources use too, sets it, and the cloud has no value: edit it by hand")
	}
	text, err := w.valueText(value, path, w.steps[min(w.attrAt, i):i])
	if err != nil {
		return w.manual(SourceSharedLocal, x.Range(), "the cloud's value can't be written: edit it by hand")
	}
	return Source{File: w.file, Range: x.Range(), Kind: SourceSharedLocal, Edit: &SourceEdit{File: w.file, Start: x.Range().Start.Byte, End: x.Range().End.Byte, Text: text}}
}

// variable is the source of an attribute var.name sets: never edited.
func (w *driftWalk) variable(x *hclsyntax.ScopeTraversalExpr, name string) Source {
	v := w.cfg.variables[name]
	switch {
	case v == nil:
		return w.manual(SourceVariable, x.Range(), "var."+name+" sets it, which the root doesn't declare: edit it by hand")
	case sensitiveVariable(v):
		return w.manual(SourceSensitiveVariable, x.Range(), fmt.Sprintf("a secret, which var.%s sets: update it in your secret store, or TF_VAR_%s", name, name))
	case hasDefault(v):
		return w.manual(SourceVariable, x.Range(), "var."+name+" sets it, with another root's value as its default: update it from that root")
	}
	return w.manual(SourceVariable, x.Range(), "var."+name+" sets it: change the value the root is given")
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

// reference is the source of an attribute x, a reference, sets: if the
// cloud's value is another managed resource's ID, ARN or name, a
// reference to it; else a hand edit.
func (w *driftWalk) reference(x *hclsyntax.ScopeTraversalExpr, path []string, i int) Source {
	ref := w.text(x.Range())
	if value, ok := w.c.value.(string); ok && value != "" && i == len(w.steps) {
		if target, ok := uniqueTarget(path[len(path)-1], value, w.e.refs[value]); ok && target.address != w.address {
			text := hclwrite.TokensForTraversal(target.traversal()).Bytes()
			if string(text) != ref {
				return Source{File: w.file, Range: x.Range(), Kind: SourceReference, Edit: &SourceEdit{File: w.file, Start: x.Range().Start.Byte, End: x.Range().End.Byte, Text: text}}
			}
		}
	}
	return w.manual(SourceReference, x.Range(), "it refers to "+ref+", and the cloud's value isn't another managed resource's: edit it by hand")
}

// valueText writes value as HCL, typed as the schema types the argument
// at path and the steps sub into it: port = 80, not "80"; objects
// without their null attributes.
func (w *driftWalk) valueText(value any, path []string, sub []pathStep) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var v cty.Value
	ty := w.typeAt(path, sub)
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
	ty := schemaType(w.e.schemas, "resource", w.c.Type, path)
	for _, s := range sub {
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
func (w *driftWalk) insertArgument(block *hclsyntax.Block, name string, text []byte) *SourceEdit {
	src := w.f.src
	line := slices.Concat([]byte(name+" = "), text, []byte("\n"))
	opening, closing := block.OpenBraceRange, block.CloseBraceRange
	if opening.Start.Line == closing.Start.Line {
		if len(block.Body.Attributes)+len(block.Body.Blocks) > 0 {
			return nil
		}
		return &SourceEdit{File: w.file, Start: opening.End.Byte, End: closing.Start.Byte, Text: slices.Concat([]byte("\n"), line), Layout: w.address}
	}
	at := lineStart(src, closing.Start.Byte)
	if len(bytes.TrimSpace(src[at:closing.Start.Byte])) > 0 {
		return nil
	}
	return &SourceEdit{File: w.file, Start: at, End: at, Text: line, Layout: w.address}
}

// insertKey adds the key with the cloud's value to obj, an object
// literal.
func (w *driftWalk) insertKey(obj *hclsyntax.ObjectConsExpr, key string, path []string, i int) Source {
	if w.c.value == nil {
		return w.manual(SourceLiteral, obj.Range(), "the code doesn't have the key the cloud removed")
	}
	text, err := w.valueText(w.c.value, path, w.steps[min(w.attrAt, i):i])
	if err != nil {
		return w.manual(SourceLiteral, obj.Range(), "the cloud's value can't be written: edit it by hand")
	}
	item := keyText(key) + " = " + string(text)
	src := w.f.src
	rng := obj.Range()
	end := rng.End.Byte - 1 // the closing brace
	var edit *SourceEdit
	switch {
	case obj.OpenRange.Start.Line == rng.End.Line && len(obj.Items) == 0:
		edit = &SourceEdit{File: w.file, Start: rng.Start.Byte, End: rng.End.Byte, Text: []byte("{\n" + item + "\n}")}
	case obj.OpenRange.Start.Line == rng.End.Line:
		after := obj.Items[len(obj.Items)-1].ValueExpr.Range().End.Byte
		edit = &SourceEdit{File: w.file, Start: after, End: after, Text: []byte(", " + item)}
	default:
		at := lineStart(src, end)
		if len(bytes.TrimSpace(src[at:end])) > 0 {
			return w.manual(SourceLiteral, rng, "add the key by hand")
		}
		edit = &SourceEdit{File: w.file, Start: at, End: at, Text: []byte(item + "\n")}
	}
	return Source{File: w.file, Range: rng, Kind: SourceLiteral, Edit: edit}
}

// appendMergeArg adds { key = value } as the last argument of call, a
// merge, so that the resource's value wins.
func (w *driftWalk) appendMergeArg(call *hclsyntax.FunctionCallExpr, key string, path []string, i int) Source {
	if w.c.value == nil {
		return w.manual(SourceExpression, call.Range(), "merge() sets it, and the cloud removed the key: edit it by hand")
	}
	text, err := w.valueText(w.c.value, path, w.steps[min(w.attrAt, i):i])
	if err != nil {
		return w.manual(SourceExpression, call.Range(), "the cloud's value can't be written: edit it by hand")
	}
	at := call.CloseParenRange.Start.Byte
	return Source{File: w.file, Range: call.Range(), Kind: SourceLiteral, Edit: &SourceEdit{File: w.file, Start: at, End: at, Text: []byte(", { " + keyText(key) + " = " + string(text) + " }")}}
}

// removeLines removes the lines of rng, if nothing else is on them.
func (w *driftWalk) removeLines(rng hcl.Range) *SourceEdit {
	src := w.f.src
	start, end := lineStart(src, rng.Start.Byte), lineEnd(src, rng.End.Byte)
	if len(bytes.TrimSpace(src[start:rng.Start.Byte])) > 0 || len(bytes.TrimSpace(src[rng.End.Byte:end])) > 0 {
		return nil
	}
	return &SourceEdit{File: w.file, Start: start, End: end}
}

// removeItem removes item from obj: its lines, if it is alone on them,
// else the item and the comma after it.
func (w *driftWalk) removeItem(obj *hclsyntax.ObjectConsExpr, item *hclsyntax.ObjectConsItem) *SourceEdit {
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
	return &SourceEdit{File: w.file, Start: rng.Start.Byte, End: end}
}

// apply makes in contents, files by name, the edits that take the
// cloud's values for changes, in order, each found again in the files as
// the edits before it left them; each edited file is laid out where an
// argument was added, and formatted, as terraform fmt would. It returns
// the names of the files it edited, sorted, the sources it edited by,
// and the sources of the changes it couldn't edit: a hand edit, or a
// secret, which includes an edit that would add a value the literal
// secrets check takes for one (see literalSecrets).
func (e *driftEditor) apply(contents map[string][]byte, changes []*DriftChange) ([]string, map[*DriftChange]Source, map[*DriftChange]Source, error) {
	edited := map[string]bool{}
	applied := map[*DriftChange]Source{}
	failed := map[*DriftChange]Source{}
	for _, c := range changes {
		cfg, err := parseDriftConfig(contents)
		if err != nil {
			return nil, nil, nil, err
		}
		src := e.resolve(cfg, c)
		if src.Edit == nil {
			failed[c] = src
			continue
		}
		name := src.Edit.File
		old := contents[name]
		next := slices.Concat(old[:src.Edit.Start], src.Edit.Text, old[src.Edit.End:])
		if src.Edit.Layout != "" {
			next = layoutResource(next, src.Edit.Layout)
		}
		next = hclwrite.Format(next)
		if _, diags := hclsyntax.ParseConfig(next, name, hcl.InitialPos); diags.HasErrors() {
			src.Edit, src.Reason = nil, "the edit wouldn't parse: edit it by hand"
			failed[c] = src
			continue
		}
		if len(literalSecrets(&parsedFile{rel: name, content: string(next)})) > len(literalSecrets(&parsedFile{rel: name, content: string(old)})) {
			src.Edit, src.Secret = nil, true
			src.Reason = "a secret, which is never written into the code: move it into a sensitive variable by hand, and set that from your secret store"
			failed[c] = src
			continue
		}
		contents[name] = next
		edited[name] = true
		applied[c] = src
	}
	return sortedKeys(edited), applied, failed, nil
}

// layoutResource lays out the block of the resource at address in src
// (see layoutBody), and only that block.
func layoutResource(src []byte, address string) []byte {
	file, diags := hclsyntax.ParseConfig(src, "", hcl.InitialPos)
	if diags.HasErrors() {
		return src
	}
	for _, b := range file.Body.(*hclsyntax.Body).Blocks {
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

// reportSource is src as a report shows it: the file and line, and the
// expression if on one line, with secrets left out; nil without a file.
func reportSource(cfg *driftConfig, src Source, forms []string) *DriftSource {
	if src.File == "" {
		return nil
	}
	out := &DriftSource{File: src.File, Line: src.Range.Start.Line}
	f := cfg.files[src.File]
	if f == nil || src.Kind == SourceAbsent || src.Kind == SourceResource {
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
