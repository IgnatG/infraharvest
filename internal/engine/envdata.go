// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// stackData is a data source the roots of a stack read, by its address
// (type.name), with its arguments merged as a resource's are (see
// mergeBody).
type stackData struct {
	address string
	body    *mergedBody
	// deploy is, for a data source only some environments read, the
	// deploy_<feature> variable of the feature that alone reads it, which
	// counts it too (see dataGates).
	deploy *envVariable
}

// mergeData merges the data sources the roots read, by address, as the
// merged root reads them. A data source only some environments read is
// counted by the deploy variable of the feature that alone refers to it
// (see dataGates). It returns why they don't merge, if they don't: one
// only some environments read that no one feature alone refers to, or
// one whose arguments don't merge.
func (m *stackMerge) mergeData() string {
	byAddress := map[string][]*envResource{}
	for e, root := range m.roots {
		for _, b := range dataBlocksOf(root.data) {
			if byAddress[b.Address] == nil {
				byAddress[b.Address] = make([]*envResource, len(m.roots))
			}
			typ, _, _ := strings.Cut(b.Address, ".")
			byAddress[b.Address][e] = &envResource{env: e, block: b, resourceType: typ}
		}
	}
	gates := m.dataGates(byAddress)
	for _, address := range slices.Sorted(maps.Keys(byAddress)) {
		members := byAddress[address]
		if slices.Contains(members, nil) && gates[address] == nil {
			return fmt.Sprintf("data.%s is read in some environments only, and not by the resources of one feature alone", address)
		}
		typ, name, _ := strings.Cut(address, ".")
		body, reason := m.mergeBody(&stackResource{resourceType: typ, label: name, members: members})
		if reason != "" {
			return fmt.Sprintf("data.%s: %s", address, reason)
		}
		m.rootData = append(m.rootData, &stackData{address: address, body: body, deploy: gates[address]})
	}
	return ""
}

// dataGates returns, of the data sources by address that only some
// environments read, those one feature alone refers to, with its deploy
// variable: in every environment that reads the data source, only the
// feature's resources, or data sources the feature counts too, refer to
// it, and the feature is in those environments alone.
func (m *stackMerge) dataGates(byAddress map[string][]*envResource) map[string]*envVariable {
	resources := map[string][]*stackResource{}
	data := map[string][]string{}
	for _, r := range m.resources {
		for _, member := range r.members {
			if member == nil {
				continue
			}
			for _, address := range dataReferences(member.block.Syntax.Body) {
				resources[address] = append(resources[address], r)
			}
		}
	}
	for _, members := range byAddress {
		for _, member := range members {
			if member == nil {
				continue
			}
			for _, address := range dataReferences(member.block.Syntax.Body) {
				if address != member.block.Address {
					data[address] = append(data[address], member.block.Address)
				}
			}
		}
	}
	gates := map[string]*envVariable{}
	// A data source that only gated data sources refer to is gated once
	// they are.
	for changed := true; changed; {
		changed = false
		for _, address := range slices.Sorted(maps.Keys(byAddress)) {
			members := byAddress[address]
			if !slices.Contains(members, nil) || gates[address] != nil {
				continue
			}
			coverage := coverageKey(&stackResource{members: members})
			var deploy *envVariable
			ok := len(resources[address])+len(data[address]) > 0
			for _, r := range resources[address] {
				switch {
				case r.deploy == nil || coverageKey(r) != coverage:
					ok = false
				case deploy == nil:
					deploy = r.deploy
				case deploy != r.deploy:
					ok = false
				}
			}
			for _, d := range data[address] {
				switch {
				case gates[d] == nil:
					ok = false
				case deploy == nil:
					deploy = gates[d]
				case deploy != gates[d]:
					ok = false
				}
			}
			if ok && deploy != nil {
				gates[address] = deploy
				changed = true
			}
		}
	}
	return gates
}

// dataReferences returns the data sources body refers to, by address
// (type.name), sorted.
func dataReferences(body *hclsyntax.Body) []string {
	var addresses []string
	for _, t := range hclfile.Traversals(body) {
		if t.RootName() != "data" || len(t) < 3 {
			continue
		}
		typ, ok := t[1].(hcl.TraverseAttr)
		name, named := t[2].(hcl.TraverseAttr)
		if ok && named {
			addresses = append(addresses, typ.Name+"."+name.Name)
		}
	}
	slices.Sort(addresses)
	return slices.Compact(addresses)
}

// indexGatedData makes each reference of the merged root's resources and
// data sources to a data source a deploy variable counts, data.type.name,
// one to its instance, data.type.name[0].
func (m *stackMerge) indexGatedData() {
	gated := map[string]bool{}
	for _, d := range m.rootData {
		if d.deploy != nil {
			gated[d.address] = true
		}
	}
	if len(gated) == 0 {
		return
	}
	index := func(a *mergedAttribute) { a.value = indexDataTokens(a.value, gated) }
	for _, r := range m.resources {
		if r.body != nil {
			r.body.visit(index, nil)
		}
	}
	for _, g := range m.forEach {
		g.body.visit(index, nil)
	}
	for _, d := range m.rootData {
		d.body.visit(index, nil)
	}
}

// indexDataTokens returns tokens with each reference to a data source of
// gated, data.type.name not already indexed, made data.type.name[0].
func indexDataTokens(tokens hclwrite.Tokens, gated map[string]bool) hclwrite.Tokens {
	is := func(i int, typ hclsyntax.TokenType) bool { return i < len(tokens) && tokens[i].Type == typ }
	out := make(hclwrite.Tokens, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		out = append(out, tokens[i])
		if !is(i, hclsyntax.TokenIdent) || string(tokens[i].Bytes) != "data" || (i > 0 && is(i-1, hclsyntax.TokenDot)) {
			continue
		}
		if !is(i+1, hclsyntax.TokenDot) || !is(i+2, hclsyntax.TokenIdent) || !is(i+3, hclsyntax.TokenDot) || !is(i+4, hclsyntax.TokenIdent) || is(i+5, hclsyntax.TokenOBrack) {
			continue
		}
		if !gated[string(tokens[i+2].Bytes)+"."+string(tokens[i+4].Bytes)] {
			continue
		}
		out = append(out, tokens[i+1:i+5]...)
		out = append(out,
			&hclwrite.Token{Type: hclsyntax.TokenOBrack, Bytes: []byte("[")},
			&hclwrite.Token{Type: hclsyntax.TokenNumberLit, Bytes: []byte("0")},
			&hclwrite.Token{Type: hclsyntax.TokenCBrack, Bytes: []byte("]")},
		)
		i += 4
	}
	return out
}
