// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	ctyjson "github.com/zclconf/go-cty/cty/json"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
)

// OmitArguments removes from the resources in path the arguments omit
// names for their type, under "*" for every type, or under a resource's
// address (type.name) for that resource alone, whether written as
// attributes or as blocks (see omission for the entries' form). Whether
// an entry applies is decided on the resource as generated, before
// anything is removed. It reports whether it removed anything. Only the
// catalogue's golden test calls it outside the engine, which is why it is
// exported.
func OmitArguments(path string, omit map[string][]string) (bool, error) {
	if len(omit) == 0 {
		return false, nil
	}
	return rewrite(path, func(f *hclwrite.File, syntax *hclsyntax.Body) bool {
		changed := false
		for i, block := range f.Body().Blocks() {
			if block.Type() != "resource" || len(block.Labels()) != 2 || i >= len(syntax.Blocks) {
				continue
			}
			var found []removal
			for _, o := range omissions(omit, block.Labels()[0], block.Labels()[1]) {
				found = append(found, o.find(syntax.Blocks[i].Body)...)
			}
			if removeAll(block.Body(), found) {
				changed = true
			}
		}
		return changed
	})
}

// omission is an entry of a catalogue's omit list:
//
//   - name leaves the argument or block out whatever it is set to;
//   - name=value leaves the attribute out only where it is set to value,
//     such as a default the cloud reports;
//   - a.b leaves out b in every nested block a (a.b=value likewise), and
//     *.b in every nested block of any type, such as the tags_all AWS
//     computes in an instance's root_block_device;
//   - any of these followed by " when " and conditions joined by " and "
//     applies only where every condition holds: path (set, not null),
//     path=value, or !path (not set). A condition's path starting with the
//     blocks the argument is in refers to those blocks, else to the
//     resource, where it holds if it holds in any of the blocks it names:
//     stickiness when stickiness.enabled=false.
//
// A value is compared as a string (true, 3, "text"). In it, {*} stands for
// any text and {argument} for the value of one of the resource's string
// arguments. A value that is a JSON object or array is compared as JSON,
// against the attribute's JSON, such as a policy: keys and values alike,
// with arrays in any order.
type omission struct {
	path   []string
	value  string
	always bool
	when   []omitCondition
}

// omitCondition is a condition of an omission (see omission).
type omitCondition struct {
	path   []string
	value  string
	valued bool
	negate bool
}

// parseOmission reads an omit entry (see omission).
func parseOmission(entry string) omission {
	target, conditions, hasConditions := strings.Cut(entry, " when ")
	name, value, withValue := strings.Cut(strings.TrimSpace(target), "=")
	o := omission{path: strings.Split(name, "."), value: value, always: !withValue}
	if !hasConditions {
		return o
	}
	for _, c := range strings.Split(conditions, " and ") {
		c = strings.TrimSpace(c)
		negate := strings.HasPrefix(c, "!")
		c = strings.TrimPrefix(c, "!")
		name, value, valued := strings.Cut(c, "=")
		o.when = append(o.when, omitCondition{path: strings.Split(name, "."), value: value, valued: valued, negate: negate})
	}
	return o
}

// omissions returns the entries of omit for every type, for typ and for
// the resource typ.name.
func omissions(omit map[string][]string, typ, name string) []omission {
	entries := append(append([]string(nil), omit["*"]...), omit[typ]...)
	if name != "" {
		entries = append(entries, omit[typ+"."+name]...)
	}
	list := make([]omission, 0, len(entries))
	for _, entry := range entries {
		list = append(list, parseOmission(entry))
	}
	return list
}

// omitBody is a body an omission looks into: a resource's, or a nested
// block's, by its type.
type omitBody struct {
	typ  string
	body *hclsyntax.Body
}

// removal is an argument an omission leaves out: in the nested blocks
// steps lead to, the attribute name, or, with block at or above zero, the
// block of type name at that index among the blocks of its type.
type removal struct {
	steps []removalStep
	name  string
	block int
	rng   hcl.Range
}

// removalStep is a nested block: the index-th of its type.
type removalStep struct {
	typ   string
	index int
}

// path names what r removes, such as stickiness or root_block_device.iops.
func (r removal) path() string {
	parts := make([]string, 0, len(r.steps)+1)
	for _, s := range r.steps {
		parts = append(parts, s.typ)
	}
	return strings.Join(append(parts, r.name), ".")
}

// find returns what o leaves out of resource, a resource's body.
func (o omission) find(resource *hclsyntax.Body) []removal {
	var found []removal
	o.findIn(resource, []omitBody{{body: resource}}, nil, o.path, &found)
	return found
}

func (o omission) findIn(resource *hclsyntax.Body, chain []omitBody, steps []removalStep, path []string, found *[]removal) {
	body := chain[len(chain)-1].body
	if len(path) == 1 {
		name := path[0]
		if attr, ok := body.Attributes[name]; ok && (o.always || matchesValue(attr.Expr, o.value, resource)) && o.holds(resource, chain) {
			*found = append(*found, removal{steps: steps, name: name, block: -1, rng: attr.SrcRange})
		}
		if !o.always {
			return
		}
		index := 0
		for _, b := range body.Blocks {
			if b.Type != name {
				continue
			}
			if o.holds(resource, append(append([]omitBody(nil), chain...), omitBody{typ: name, body: b.Body})) {
				*found = append(*found, removal{steps: steps, name: name, block: index, rng: b.DefRange()})
			}
			index++
		}
		return
	}
	// The index of each block among the blocks of its type.
	indexes := map[string]int{}
	for _, b := range body.Blocks {
		index := indexes[b.Type]
		indexes[b.Type]++
		if path[0] != "*" && b.Type != path[0] {
			continue
		}
		next := append(append([]removalStep(nil), steps...), removalStep{typ: b.Type, index: index})
		o.findIn(resource, append(append([]omitBody(nil), chain...), omitBody{typ: b.Type, body: b.Body}), next, path[1:], found)
	}
}

// holds reports whether o's conditions hold for what it found in chain,
// the scopes from the resource down.
func (o omission) holds(resource *hclsyntax.Body, chain []omitBody) bool {
	for _, c := range o.when {
		// The blocks the condition's path shares with the chain are those
		// of the argument; the rest is looked up from the last of them.
		j := 0
		for j+1 < len(chain) && j+1 < len(c.path) && c.path[j] == chain[j+1].typ {
			j++
		}
		if c.holdsIn(resource, chain[j].body, c.path[j:]) == c.negate {
			return false
		}
	}
	return true
}

// holdsIn reports whether the condition's path, from body, is set (to its
// value, if it has one), in any of the nested blocks the path names.
func (c omitCondition) holdsIn(resource, body *hclsyntax.Body, path []string) bool {
	if len(path) == 1 {
		if attr, ok := body.Attributes[path[0]]; ok {
			if c.valued {
				return matchesValue(attr.Expr, c.value, resource)
			}
			v, diags := attr.Expr.Value(hclfile.EvalContext)
			return diags.HasErrors() || !v.IsNull()
		}
		if c.valued {
			return false
		}
		for _, b := range body.Blocks {
			if b.Type == path[0] {
				return true
			}
		}
		return false
	}
	for _, b := range body.Blocks {
		if b.Type == path[0] && c.holdsIn(resource, b.Body, path[1:]) {
			return true
		}
	}
	return false
}

// matchesValue reports whether expr is a constant whose value matches
// pattern (see omission), in resource, a resource's body, if there is one.
func matchesValue(expr hclsyntax.Expression, pattern string, resource *hclsyntax.Body) bool {
	if len(expr.Variables()) > 0 {
		return false
	}
	v, diags := expr.Value(hclfile.EvalContext)
	if diags.HasErrors() || v.IsNull() || !v.IsWhollyKnown() {
		return false
	}
	if trimmed := strings.TrimSpace(pattern); strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var want any
		if err := json.Unmarshal([]byte(trimmed), &want); err == nil {
			got, ok := jsonOf(v)
			return ok && matchesJSON(want, got, resource)
		}
	}
	s, err := convert.Convert(v, cty.String)
	if err != nil || s.IsNull() {
		return false
	}
	return matchesString(pattern, s.AsString(), resource)
}

// jsonOf returns v as decoded JSON: the JSON a string holds, or the JSON
// of any other value.
func jsonOf(v cty.Value) (any, bool) {
	var raw []byte
	if v.Type() == cty.String {
		raw = []byte(v.AsString())
	} else {
		var err error
		if raw, err = ctyjson.Marshal(v, v.Type()); err != nil {
			return nil, false
		}
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, false
	}
	return decoded, true
}

// matchesJSON reports whether got, decoded JSON, matches want: strings as
// patterns (see matchesString), objects with the same keys, and arrays
// with the same elements in any order.
func matchesJSON(want, got any, resource *hclsyntax.Body) bool {
	switch w := want.(type) {
	case string:
		g, ok := got.(string)
		return ok && matchesString(w, g, resource)
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok || !matchesJSON(wv, gv, resource) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		used := make([]bool, len(g))
		for _, wv := range w {
			found := false
			for i, gv := range g {
				if !used[i] && matchesJSON(wv, gv, resource) {
					used[i], found = true, true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	default:
		return want == got
	}
}

// valueField is a {*} or an {argument} in a value (see omission).
var valueField = regexp.MustCompile(`\{(\*|[a-z][a-z0-9_]*)\}`)

// matchesString reports whether s matches pattern, in which {*} stands for
// any text and {argument} for the value of the resource's string argument
// of that name. A pattern naming an argument that isn't a literal string
// matches nothing.
func matchesString(pattern, s string, resource *hclsyntax.Body) bool {
	if !strings.Contains(pattern, "{") {
		return pattern == s
	}
	var expr strings.Builder
	expr.WriteString("^")
	last := 0
	for _, m := range valueField.FindAllStringSubmatchIndex(pattern, -1) {
		expr.WriteString(regexp.QuoteMeta(pattern[last:m[0]]))
		switch field := pattern[m[2]:m[3]]; field {
		case "*":
			expr.WriteString("(?s:.*)")
		default:
			value, ok := literalArgument(resource, field)
			if !ok {
				return false
			}
			expr.WriteString(regexp.QuoteMeta(value))
		}
		last = m[1]
	}
	expr.WriteString(regexp.QuoteMeta(pattern[last:]))
	expr.WriteString("$")
	re, err := regexp.Compile(expr.String())
	return err == nil && re.MatchString(s)
}

// literalArgument returns the value of a resource's argument if it is a
// literal string; there is none without a resource.
func literalArgument(resource *hclsyntax.Body, name string) (string, bool) {
	if resource == nil {
		return "", false
	}
	attr, ok := resource.Attributes[name]
	if !ok || len(attr.Expr.Variables()) > 0 {
		return "", false
	}
	v, diags := attr.Expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.IsKnown() || v.Type() != cty.String {
		return "", false
	}
	return v.AsString(), true
}

// removeAll removes from body, a resource's, what found names, and reports
// whether it removed anything.
func removeAll(body *hclwrite.Body, found []removal) bool {
	// Resolve every removal before removing any, which would shift the
	// indexes of the blocks after it.
	type target struct {
		body  *hclwrite.Body
		name  string
		block *hclwrite.Block
	}
	var targets []target
	for _, r := range found {
		parent := body
		for _, s := range r.steps {
			if parent = nthBlock(parent, s.typ, s.index); parent == nil {
				break
			}
		}
		if parent == nil {
			continue
		}
		t := target{body: parent, name: r.name}
		if r.block >= 0 {
			b := nthBlockOf(parent, r.name, r.block)
			if b == nil {
				continue
			}
			t.block = b
		}
		targets = append(targets, t)
	}
	changed := false
	for _, t := range targets {
		switch {
		case t.block != nil:
			if t.body.RemoveBlock(t.block) {
				changed = true
			}
		case t.body.RemoveAttribute(t.name) != nil:
			changed = true
		}
	}
	return changed
}

// nthBlock returns the body of the index-th nested block of type typ.
func nthBlock(body *hclwrite.Body, typ string, index int) *hclwrite.Body {
	if b := nthBlockOf(body, typ, index); b != nil {
		return b.Body()
	}
	return nil
}

func nthBlockOf(body *hclwrite.Body, typ string, index int) *hclwrite.Block {
	i := 0
	for _, b := range body.Blocks() {
		if b.Type() != typ {
			continue
		}
		if i == index {
			return b
		}
		i++
	}
	return nil
}

// withResourceOmissions returns omit with, under each import's address,
// the arguments the import itself leaves out (see Import.Omit).
func withResourceOmissions(omit map[string][]string, imports []rootfile.Import) map[string][]string {
	var with map[string][]string
	for _, imp := range imports {
		if len(imp.Omit) == 0 {
			continue
		}
		if with == nil {
			with = maps.Clone(omit)
			if with == nil {
				with = map[string][]string{}
			}
		}
		address := imp.Type + "." + imp.Name
		with[address] = append(slices.Clone(with[address]), imp.Omit...)
	}
	if with == nil {
		return omit
	}
	return with
}
