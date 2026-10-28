// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"slices"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// keysBody merges the bodies of keys, resources of one type, as keys of
// one forEach, in every environment that has each: arguments with the
// same value everywhere stay as they are, and the others, its fields,
// become each.value.<field>. It reports false where they don't merge:
// more than half of the arguments differ, or one that differs reads
// anything, or is sensitive, or the nested blocks aren't all the same.
func (m *stackMerge) keysBody(keys []*stackResource) (*mergedBody, []string, bool) {
	var in []envBody
	for _, k := range keys {
		for e, member := range k.members {
			if member != nil {
				in = append(in, envBody{e, member.block.Write.Body()})
			}
		}
	}
	typ := keys[0].resourceType
	out := &mergedBody{}
	var fields []string
	names := attributeUnion(in)
	for _, name := range names {
		values := make([]hclwrite.Tokens, len(in))
		for i, x := range in {
			if attr := x.body.GetAttribute(name); attr != nil {
				values[i] = m.renamed(x.env, attr.Expr().BuildTokens(nil))
			}
		}
		if sameExprs(values) {
			out.attributes = append(out.attributes, &mergedAttribute{name: name, kind: sameValue, value: values[0]})
			continue
		}
		for _, v := range values {
			if v == nil {
				continue
			}
			if res, vars, other := exprReads(v); res || vars || other {
				return nil, nil, false
			}
		}
		if m.sensitive(typ, []string{name}) {
			return nil, nil, false
		}
		fields = append(fields, name)
		each := hcl.Traversal{hcl.TraverseRoot{Name: "each"}, hcl.TraverseAttr{Name: "value"}, hcl.TraverseAttr{Name: name}}
		out.attributes = append(out.attributes, &mergedAttribute{name: name, kind: eachValue, value: hclwrite.TokensForTraversal(each)})
	}
	if 2*len(fields) > len(names) {
		return nil, nil, false
	}
	for _, x := range in[1:] {
		if m.blocksKey(x.env, x.body, nil) != m.blocksKey(in[0].env, in[0].body, nil) {
			return nil, nil, false
		}
	}
	for _, blk := range in[0].body.Blocks() {
		out.blocks = append(out.blocks, &mergedBlock{typ: blk.Type(), body: m.constantBody(in[0].env, blk.Body())})
	}
	return out, fields, true
}

// keysVariable returns g's variable: in each environment, a map of the
// keys it has to objects of their fields.
func (m *stackMerge) keysVariable(g *forEachGroup) *envVariable {
	types := map[string]cty.Type{}
	for _, f := range g.fields {
		types[f] = tfrun.AttributeTypeOrAny(m.schemas, g.resourceType, []string{f})
	}
	v := &envVariable{
		name:         pluralName(g.resourceType),
		resourceType: g.resourceType,
		what:         "instances of " + g.address() + " in the environment, by key",
		ty:           cty.Map(cty.Object(types)),
		values:       map[int]hclwrite.Tokens{},
	}
	for e := range m.envs {
		var keys []hclwrite.ObjectAttrTokens
		for _, k := range g.keys {
			member := k.members[e]
			if member == nil {
				continue
			}
			var fields []hclwrite.ObjectAttrTokens
			for _, f := range g.fields {
				if attr := member.block.Write.Body().GetAttribute(f); attr != nil {
					fields = append(fields, hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForIdentifier(f), Value: attr.Expr().BuildTokens(nil)})
				}
			}
			keys = append(keys, hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForValue(cty.StringVal(k.label)), Value: hclwrite.TokensForObject(fields)})
		}
		v.values[e] = hclwrite.TokensForObject(keys)
	}
	return v
}

// keysLocal returns, for g, a forEach whose keys every environment has,
// each key's fields as one merged body (see forEachGroup.localKeys), in
// the order of g.keys: a field's value where every environment has the
// same, or a template where they are the same once normalised. It
// returns nil where a key's field differs between environments
// otherwise, or some keys or environments lack it: the variable keeps
// what each environment's keys have.
func (m *stackMerge) keysLocal(g *forEachGroup) []*mergedBody {
	keys := make([]*mergedBody, len(g.keys))
	for i, k := range g.keys {
		if k.coverage() < len(m.envs) {
			return nil
		}
		body := &mergedBody{}
		for _, f := range g.fields {
			var in []envBody
			var values []hclwrite.Tokens
			for e, member := range k.members {
				attr := member.block.Write.Body().GetAttribute(f)
				if attr == nil {
					return nil
				}
				in = append(in, envBody{e, member.block.Write.Body()})
				values = append(values, attr.Expr().BuildTokens(nil))
			}
			if sameExprs(values) {
				body.attributes = append(body.attributes, &mergedAttribute{name: f, kind: sameValue, value: values[0]})
				continue
			}
			normal, ok := m.template(in, values)
			if !ok {
				return nil
			}
			body.attributes = append(body.attributes, &mergedAttribute{name: f, kind: templateValue, normal: normal})
		}
		keys[i] = body
	}
	return keys
}

// keysShape is what keysBody compares of a resource's bodies, read once
// for groupForEach to try it against many others: each body's values, as
// exprKey gives them once renamed, whether each reads anything, and the
// key of its nested blocks, the same in every body or blocksDiffer.
type keysShape struct {
	bodies []bodyShape
	blocks string
	names  []string // the arguments any body sets, sorted
	// refers are the resources its renamed values write references to:
	// once one joins a forEach, those are written differently, and the
	// shape is read again.
	refers map[*stackResource]bool
}

type bodyShape struct {
	values map[string]string
	reads  map[string]bool
}

// blocksDiffer is the blocks key of a resource whose environments' nested
// blocks differ: no key of a forEach can have it.
const blocksDiffer = "\x00differ"

// keysShape reads r's shape, as keysBody would read it now.
func (m *stackMerge) keysShape(r *stackResource) *keysShape {
	s := &keysShape{refers: map[*stackResource]bool{}}
	note := func(to *stackResource) { s.refers[to] = true }
	first := true
	for e, member := range r.members {
		if member == nil {
			continue
		}
		body := member.block.Write.Body()
		b := bodyShape{values: map[string]string{}, reads: map[string]bool{}}
		for name, attr := range body.Attributes() {
			value := m.renamedNoting(e, attr.Expr().BuildTokens(nil), note)
			res, vars, other := exprReads(value)
			b.values[name], b.reads[name] = exprKey(value), res || vars || other
			if !slices.Contains(s.names, name) {
				s.names = append(s.names, name)
			}
		}
		s.bodies = append(s.bodies, b)
		switch blocks := m.blocksKey(e, body, note); {
		case first:
			s.blocks, first = blocks, false
		case blocks != s.blocks:
			s.blocks = blocksDiffer
		}
	}
	slices.Sort(s.names)
	return s
}

// keysBucket is the resources of one type whose nested blocks are the
// same, in order, as only those can be keys of one forEach, with the
// resources that set each argument, by their index.
type keysBucket struct {
	resources []*stackResource
	setting   map[string][]int
}

func (b *keysBucket) add(r *stackResource, s *keysShape) {
	for _, name := range s.names {
		b.setting[name] = append(b.setting[name], len(b.resources))
	}
	b.resources = append(b.resources, r)
}

// count adds one to shared[i] for each of names the bucket's resource i
// sets.
func (b *keysBucket) count(shared []int, names []string) {
	for _, name := range names {
		for _, i := range b.setting[name] {
			shared[i]++
		}
	}
}

// keysTally is what keysBody checks of the bodies of a forEach's keys,
// kept as groupForEach adds keys, so that trying one more costs what its
// own arguments do.
type keysTally struct {
	resourceType string
	bodies       int
	blocks       string
	attrs        map[string]*attrTally
}

// attrTally is an argument of the bodies of a keysTally: how many set it,
// whether those all have one value, and whether any reads anything.
type attrTally struct {
	set   int
	value string
	same  bool
	reads bool
}

func newKeysTally(resourceType string, seed *keysShape) *keysTally {
	t := &keysTally{resourceType: resourceType, blocks: seed.blocks, attrs: map[string]*attrTally{}}
	t.add(seed)
	return t
}

// add tallies the bodies of s, and returns the arguments none of the keys
// set before.
func (t *keysTally) add(s *keysShape) []string {
	var added []string
	for _, b := range s.bodies {
		for name, value := range b.values {
			a := t.attrs[name]
			if a == nil {
				t.attrs[name] = &attrTally{set: 1, value: value, same: true, reads: b.reads[name]}
				added = append(added, name)
				continue
			}
			a.set++
			a.same = a.same && value == a.value
			a.reads = a.reads || b.reads[name]
		}
	}
	t.bodies += len(s.bodies)
	return added
}

// mayAdmit reports whether the tallied keys may merge with the resource
// of shape s, which sets shared of the arguments they set: each argument
// one side sets and the other doesn't differs, and no more than half of
// them all may, so the two must share a third of them. It is a cheap test
// to make before admits.
func (t *keysTally) mayAdmit(s *keysShape, shared int) bool {
	return 3*shared >= len(t.attrs)+len(s.names)
}

// admits reports whether keysBody would merge the tallied keys with the
// resource of shape s.
func (m *stackMerge) admits(t *keysTally, s *keysShape) bool {
	if t.blocks == blocksDiffer || s.blocks != t.blocks {
		return false
	}
	bodies := t.bodies + len(s.bodies)
	names, fields := 0, 0
	// differs counts name as a field, or reports false where keysBody
	// refuses one that differs.
	differs := func(name string, reads bool) bool {
		if reads || m.sensitive(t.resourceType, []string{name}) {
			return false
		}
		fields++
		return true
	}
	for name, a := range t.attrs {
		names++
		set, same, reads := a.set, a.same, a.reads
		for _, b := range s.bodies {
			if value, ok := b.values[name]; ok {
				set++
				same = same && value == a.value
				reads = reads || b.reads[name]
			}
		}
		if (set < bodies || !same) && !differs(name, reads) {
			return false
		}
	}
	// What the tallied keys don't set differs, as they lack it.
	for _, name := range s.names {
		if t.attrs[name] != nil {
			continue
		}
		names++
		reads := false
		for _, b := range s.bodies {
			reads = reads || b.reads[name]
		}
		if !differs(name, reads) {
			return false
		}
	}
	return 2*fields <= names
}
