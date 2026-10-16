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
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// OmitArguments removes from the resources in path the arguments omit
// names for their type, or under "*" for every type, whether written as
// attributes or as blocks. An entry name=value removes the attribute only
// where it is set to that value (see omission), and an entry
// block.name, the argument in the nested blocks of that type, or of any
// type for *.name. It reports whether it removed anything.
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
			// Every body an omission applies to, before anything is
			// removed: removing blocks would misalign the two trees.
			type target struct {
				o      omission
				bodies []bodyPair
			}
			var targets []target
			for _, o := range omissions(omit, block.Labels()[0]) {
				targets = append(targets, target{o, nestedBodies(bodyPair{block.Body(), syntax.Blocks[i].Body}, o.blocks)})
			}
			for _, t := range targets {
				for _, b := range t.bodies {
					if attr, ok := b.syntax.Attributes[t.o.name]; ok && t.o.matches(attr.Expr) && b.write.RemoveAttribute(t.o.name) != nil {
						changed = true
					}
					if !t.o.always {
						continue
					}
					for _, nested := range b.write.Blocks() {
						if nested.Type() == t.o.name && b.write.RemoveBlock(nested) {
							changed = true
						}
					}
				}
			}
		}
		return changed
	})
}

// bodyPair is a body in both trees.
type bodyPair struct {
	write  *hclwrite.Body
	syntax *hclsyntax.Body
}

// nestedBodies returns the bodies of the nested blocks at path in body,
// such as root_block_device, where * stands for blocks of any type; body
// itself for an empty path.
func nestedBodies(body bodyPair, path []string) []bodyPair {
	if len(path) == 0 {
		return []bodyPair{body}
	}
	var bodies []bodyPair
	blocks := body.write.Blocks()
	for i, b := range body.syntax.Blocks {
		if i < len(blocks) && (path[0] == "*" || b.Type == path[0]) {
			bodies = append(bodies, nestedBodies(bodyPair{blocks[i].Body(), b.Body}, path[1:])...)
		}
	}
	return bodies
}

// nestedSyntaxBodies is nestedBodies for a syntax tree alone.
func nestedSyntaxBodies(body *hclsyntax.Body, path []string) []*hclsyntax.Body {
	if len(path) == 0 {
		return []*hclsyntax.Body{body}
	}
	var bodies []*hclsyntax.Body
	for _, b := range body.Blocks {
		if path[0] == "*" || b.Type == path[0] {
			bodies = append(bodies, nestedSyntaxBodies(b.Body, path[1:])...)
		}
	}
	return bodies
}

// omission is an entry of a catalogue's omit list: name, which leaves the
// argument out whatever its value, or name=value, which leaves the
// attribute out only where it is set to value, such as a default the
// cloud reports that the provider rejects as an argument. Either may
// start with the types of the nested blocks the argument is in, such as
// root_block_device.tags_all, where * stands for any type.
type omission struct {
	blocks []string
	name   string
	value  string
	always bool
	when   []omitCondition
}

// omissions returns the entries of omit for every type and for typ.
func omissions(omit map[string][]string, typ string) []omission {
	var list []omission
	for _, entry := range append(append([]string(nil), omit["*"]...), omit[typ]...) {
		name, value, withValue := strings.Cut(entry, "=")
		path := strings.Split(name, ".")
		list = append(list, omission{blocks: path[:len(path)-1], name: path[len(path)-1], value: value, always: !withValue})
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
	index := 0
	for _, b := range body.Blocks {
		if b.Type != path[0] {
			continue
		}
		next := append(append([]removalStep(nil), steps...), removalStep{typ: path[0], index: index})
		o.findIn(resource, append(append([]omitBody(nil), chain...), omitBody{typ: path[0], body: b.Body}), next, path[1:], found)
		index++
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
			v, diags := attr.Expr.Value(omitEvalContext)
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

// omitEvalContext evaluates the constants of generated configuration, in
// which Terraform writes JSON strings as jsonencode(...).
var omitEvalContext = &hcl.EvalContext{Functions: map[string]function.Function{"jsonencode": stdlib.JSONEncodeFunc}}

// matchesValue reports whether expr is a constant whose value matches
// pattern (see omission), in resource, a resource's body, if there is one.
func matchesValue(expr hclsyntax.Expression, pattern string, resource *hclsyntax.Body) bool {
	if len(expr.Variables()) > 0 {
		return false
	}
	v, diags := expr.Value(omitEvalContext)
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
func withResourceOmissions(omit map[string][]string, imports []Import) map[string][]string {
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
