// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// features groups the resources some environments only have, outside a
// forEach, into features: resources the same environments have that refer
// to one another, or whose names start with the same word, such as
// aws_vpn_gateway.vpn and aws_vpn_connection.vpn_customer. One
// deploy_<feature> variable, true where an environment has the feature,
// counts every resource of it. A feature is named after the word most of
// its resources' names start with, or, for one resource, after the
// resource (see hclfile.CallName). Each residue resource is a feature of
// its own.
func (m *stackMerge) features() {
	var parts []*stackResource
	for _, r := range m.resources {
		if r.forEach == nil && r.coverage() < len(m.envs) {
			parts = append(parts, r)
		}
	}
	parent := make([]int, len(parts))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			i = parent[i]
		}
		return i
	}
	for i, a := range parts {
		for j := i + 1; j < len(parts); j++ {
			b := parts[j]
			if a.residue != "" || b.residue != "" || coverageKey(a) != coverageKey(b) {
				continue
			}
			if refersTo(a, b) || refersTo(b, a) || firstWord(a) == firstWord(b) {
				parent[find(j)] = find(i)
			}
		}
	}
	groups := map[int][]*stackResource{}
	var order []int
	for i, r := range parts {
		root := find(i)
		if groups[root] == nil {
			order = append(order, root)
		}
		groups[root] = append(groups[root], r)
	}
	for _, root := range order {
		members := groups[root]
		name := hclfile.CallName(members[0].resourceType + "." + members[0].label)
		resourceType := members[0].resourceType
		if len(members) > 1 {
			name, resourceType = commonWord(members), ""
		}
		addresses := make([]string, len(members))
		for i, r := range members {
			addresses[i] = r.resourceType + "." + r.label
		}
		v := &envVariable{
			name:         featureFlagName(name),
			resourceType: resourceType,
			what:         "whether the environment has " + strings.Join(addresses, ", "),
			ty:           cty.Bool,
			values:       map[int]hclwrite.Tokens{},
			hasDefault:   true,
			defaultValue: hclwrite.TokensForValue(cty.False),
		}
		for e := range m.envs {
			v.values[e] = hclwrite.TokensForValue(cty.BoolVal(members[0].members[e] != nil))
		}
		for _, r := range members {
			r.deploy = v
		}
	}
}

// coverageKey says which environments have r.
func coverageKey(r *stackResource) string {
	var b strings.Builder
	for _, member := range r.members {
		if member != nil {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}

// refersTo reports whether a refers to b in an environment.
func refersTo(a, b *stackResource) bool {
	for _, member := range a.members {
		if member == nil {
			continue
		}
		for _, t := range member.refs {
			if t.stack == b {
				return true
			}
		}
	}
	return false
}
