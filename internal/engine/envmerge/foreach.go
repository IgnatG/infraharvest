// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
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
		if m.blocksKey(x.env, x.body) != m.blocksKey(in[0].env, in[0].body) {
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
