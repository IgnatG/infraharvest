// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/run"
)

// mapClusters builds each adapter's clusters in generated.tf, anchors in
// address order, and maps them. A resource joins the first cluster whose
// anchor it refers to, if that cluster has no member of its type yet or
// the type may repeat. It returns the clusters the adapters mapped and the
// ones they declined.
func mapClusters(ws *run.Workspace, list []adapters.Adapter, taken Names, values map[string]map[string]any) ([]*trial, []ModuleCall, error) {
	generated, err := ws.Load(rootfile.GeneratedFileName)
	if err != nil {
		return nil, nil, err
	}
	resources := generated.Resources()
	// Every anchor and every trial asks about the same references: read
	// them once.
	refs := indexReferences(resources)
	clustered := map[string]bool{}
	used := map[string]bool{}
	for _, b := range generated.Syntax.Blocks {
		if b.Type == "module" && len(b.Labels) == 1 {
			used[b.Labels[0]] = true
		}
	}
	for name := range taken {
		if call, ok := strings.CutPrefix(name, "module."); ok {
			used[call] = true
		}
	}
	var trials []*trial
	var declined []ModuleCall
	for ai := range list {
		a := &list[ai]
		grouped := map[string]bool{}
		for _, anchor := range resources {
			if clustered[anchor.Address] || AddressType(anchor.Address) != a.Anchor {
				continue
			}
			c := adapters.Cluster{Anchor: resourceOf(anchor)}
			// refers reports whether r joins the cluster as one of Members:
			// by referring to the anchor, or, for the anchor's type in a
			// group, by having its key.
			refers := func(r hclfile.Resource) bool {
				return refs.refers[r.Address][anchor.Address]
			}
			if a.Group != nil {
				key, ok := a.Group(c.Anchor)
				if !ok || grouped[key] {
					continue
				}
				// A group is tried once: when it is declined, its other
				// resources don't anchor it again.
				grouped[key] = true
				refers = func(r hclfile.Resource) bool {
					if AddressType(r.Address) != a.Anchor {
						return refs.refers[r.Address][anchor.Address]
					}
					k, ok := a.Group(resourceOf(r))
					return ok && k == key
				}
			}
			types := map[string]bool{}
			in := []hclfile.Resource{anchor}
			// join adds r to the cluster if it is of one of kinds, linked to
			// it, and in no cluster yet.
			join := func(r hclfile.Resource, kinds []string, linked func(hclfile.Resource) bool) bool {
				typ := AddressType(r.Address)
				if clustered[r.Address] || slices.ContainsFunc(in, func(b hclfile.Resource) bool { return b.Address == r.Address }) ||
					(types[typ] && !slices.Contains(a.Repeated, typ)) || !slices.Contains(kinds, typ) || !linked(r) {
					return false
				}
				types[typ] = true
				in = append(in, r)
				c.Members = append(c.Members, resourceOf(r))
				return true
			}
			// Without a group, only what refers to the anchor can join.
			candidates := resources
			if a.Group == nil {
				candidates = refs.referrers(anchor.Address, resources)
			}
			for _, r := range candidates {
				join(r, a.Members, refers)
			}
			// Then the members that join through the others, until none does.
			for joined := len(a.Through) > 0; joined; {
				joined = false
				for _, r := range resources {
					if join(r, a.Through, func(r hclfile.Resource) bool { return refs.linked(r.Address, in) }) {
						joined = true
					}
				}
			}
			slices.SortFunc(c.Members, func(x, y adapters.Resource) int { return strings.Compare(x.Address(), y.Address()) })
			// Members that use another configuration of the provider are
			// mapped without it, and the call passes it to the module.
			provider, setting := sharedProvider(c)
			setProvider(setting, nil)
			call, err := a.Map(c)
			t := &trial{adapter: a, cluster: c, call: call, provider: provider}
			if err != nil {
				setProvider(setting, provider)
			}
			if adapters.IsDecline(err) {
				declined = append(declined, ModuleCall{Source: a.Source, Version: a.Version, Resources: t.members(), Declined: err.Error()})
				continue
			}
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", a.Source, err)
			}
			t.cluster.Members = slices.DeleteFunc(slices.Clone(c.Members), func(m adapters.Resource) bool { return slices.Contains(call.Left, m.Address()) })
			// What the call leaves in the root keeps its configuration.
			setProvider(slices.DeleteFunc(slices.Clone(setting), func(r adapters.Resource) bool { return !slices.Contains(call.Left, r.Address()) }), provider)
			if reason := unfollowable(t, refs, values); reason != "" {
				setProvider(setting, provider)
				declined = append(declined, ModuleCall{Source: a.Source, Version: a.Version, Resources: t.members(), Declined: reason})
				continue
			}
			name := hclfile.CallName(anchor.Address)
			if call.Name != "" {
				name = call.Name
			}
			t.name = uniqueName(name, used)
			for _, m := range t.members() {
				clustered[m] = true
			}
			trials = append(trials, t)
		}
	}
	return trials, declined, nil
}

// clusterReferences has the references between the resources of a root,
// read once: mapClusters asks about them for every anchor and every
// trial, and walking every resource's body each time made building
// clusters quadratic in the resources of a root.
type clusterReferences struct {
	// refers has, by address, the resources each resource refers to.
	refers map[string]map[string]bool
	// incoming has, by address, the references to each resource, in the
	// order of the resources they are in.
	incoming map[string][]reference
	// position has each resource's place in the root, by address.
	position map[string]int
}

// reference is a traversal in the resource at from.
type reference struct {
	from      string
	traversal hcl.Traversal
}

// indexReferences reads the references of resources (see
// referencedAddresses and hclfile.Traversals).
func indexReferences(resources []hclfile.Resource) clusterReferences {
	idx := clusterReferences{refers: map[string]map[string]bool{}, incoming: map[string][]reference{}, position: map[string]int{}}
	for i, r := range resources {
		idx.position[r.Address] = i
		refers := map[string]bool{}
		for _, tr := range hclfile.Traversals(r.Syntax.Body) {
			target := hclfile.ResourceAddress(tr)
			if target == "" {
				continue
			}
			refers[target] = true
			idx.incoming[target] = append(idx.incoming[target], reference{from: r.Address, traversal: tr})
		}
		idx.refers[r.Address] = refers
	}
	return idx
}

// referrers returns the resources of resources, those of the root in
// their order, that refer to the resource at address, in that order.
func (idx clusterReferences) referrers(address string, resources []hclfile.Resource) []hclfile.Resource {
	var referring []hclfile.Resource
	// incoming has the references of each resource next to each other.
	for _, ref := range idx.incoming[address] {
		r := resources[idx.position[ref.from]]
		if len(referring) == 0 || referring[len(referring)-1].Address != r.Address {
			referring = append(referring, r)
		}
	}
	return referring
}

// linked reports whether the resource at address refers to one of
// cluster, or one of cluster refers to it.
func (idx clusterReferences) linked(address string, cluster []hclfile.Resource) bool {
	for _, b := range cluster {
		if idx.refers[address][b.Address] || idx.refers[b.Address][address] {
			return true
		}
	}
	return false
}

func resourceOf(r hclfile.Resource) adapters.Resource {
	typ, name, _ := strings.Cut(r.Address, ".")
	return adapters.Resource{Type: typ, Name: name, Body: r.Write.Body()}
}

func uniqueName(base string, used map[string]bool) string {
	name := base
	for n := 2; used[name]; n++ {
		name = fmt.Sprintf("%s_%d", base, n)
	}
	used[name] = true
	return name
}

// unfollowable returns why references to a trial's members, from the
// resources that stay and from the call's own arguments, can't all become
// references to the module's outputs or, for a string the module has no
// output for, its imported value in values (see trial.literals), or "".
// refs has the references of the root's resources.
func unfollowable(t *trial, refs clusterReferences, values map[string]map[string]any) string {
	members := t.members()
	var traversals []hcl.Traversal
	// Only the references to members matter: those from the resources
	// that stay, in the order of the resources.
	var incoming []reference
	for _, m := range members {
		for _, ref := range refs.incoming[m] {
			if !slices.Contains(members, ref.from) {
				incoming = append(incoming, ref)
			}
		}
	}
	slices.SortStableFunc(incoming, func(x, y reference) int { return refs.position[x.from] - refs.position[y.from] })
	for _, ref := range incoming {
		traversals = append(traversals, ref.traversal)
	}
	for _, arg := range t.call.Arguments {
		expr, diags := hclsyntax.ParseExpression(arg.Value.Bytes(), "", hcl.InitialPos)
		if diags.HasErrors() {
			return fmt.Sprintf("argument %s doesn't parse: %v", arg.Name, diags)
		}
		traversals = append(traversals, expr.Variables()...)
	}
	for _, tr := range traversals {
		target := hclfile.ResourceAddress(tr)
		if !slices.Contains(members, target) {
			continue
		}
		if len(tr) < 3 {
			return fmt.Sprintf("a reference to the whole of %s", target)
		}
		attr, ok := tr[2].(hcl.TraverseAttr)
		if ok && t.call.Outputs[target][attr.Name] != "" {
			continue
		}
		// A reference to a string the module has no output for becomes
		// the string again.
		if v, isString := values[target][attr.Name].(string); ok && isString && len(tr) == 3 {
			if t.literals == nil {
				t.literals = map[string]string{}
			}
			t.literals[target+"."+attr.Name] = v
			continue
		}
		return fmt.Sprintf("the module has no output for a reference to %s", hclwrite.TokensForTraversal(tr[:3]).Bytes())
	}
	return ""
}
