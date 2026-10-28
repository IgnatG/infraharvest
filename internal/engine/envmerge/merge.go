// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package envmerge merges the finished roots of a stack's environments
// into the root they share, with what differs between them in each
// environment's var files, and works on that root afterwards: it verifies
// it as one environment, writes its moved blocks, hands its resources
// over to other roots, and gives back each environment's blocks as its
// own root would have them. The checks of the merged root's gate are the
// engine's, which the caller gives it (MergeOptions.Gate). It never
// writes state and never applies.
package envmerge

import (
	"fmt"
	"maps"
	"slices"
	"sort"

	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

// The variables a merged root always has, or has for its placeholders.
const (
	envVariableName           = "env"
	resourceGroupVariableName = "resource_group_name"
)

// stackMerge is how the roots of one stack, one per environment, become
// one root whose code every environment shares, with what differs in
// variables each environment sets.
type stackMerge struct {
	provider string
	profile  MergeProfile
	roots    []envRoot
	envs     []string      // the environments, as the roots are
	norms    []*normaliser // by environment
	index    []map[string]*envResource
	schemas  *tfjson.ProviderSchemas
	// workload starts local.naming_prefix, "<workload>-${var.env}": what
	// most names holding the environment's token have before it; "" for
	// none.
	workload  string
	resources []*stackResource // by address
	forEach   []*forEachGroup  // by address
	variables []*envVariable   // by name
	data      []string         // the data sources values read, such as aws_caller_identity.current
	// locals are the merged root's locals, in order: naming_prefix, the
	// scope's and region's IDs from data sources, and each resource's name
	// (see mergedAttribute.local).
	locals []namedLocal
	// rootData are the data sources the roots read, merged (see
	// mergeData).
	rootData []*stackData
	// reason says why the roots can't share one root at all, such as a
	// data source some environments only read; "" if they can.
	reason   string
	controls *mergeControls
}

// namedLocal is a local value of the merged root.
type namedLocal struct {
	name  string
	value hclwrite.Tokens
}

// valueKind is how the merged root writes an argument.
type valueKind int

const (
	sameValue      valueKind = iota // the same in every environment, as it is
	templateValue                   // the same once normalised, with var.env and locals
	variableValue                   // a variable
	referenceValue                  // a reference to a resource some environments lack, null where they do
	eachValue                       // each.value.<name>, in a forEach
	conditionValue                  // a condition on var.env, where one environment's value differs (see envCondition)
)

// mergedBody is a block's body across the environments that have it.
type mergedBody struct {
	attributes []*mergedAttribute // by name
	blocks     []*mergedBlock     // in the first environment's order
}

// mergedAttribute is an argument of a merged body.
type mergedAttribute struct {
	name string
	kind valueKind
	// value is what the merged root writes: the value, with references
	// to the merged root's resources.
	value    hclwrite.Tokens
	normal   hclwrite.Tokens // templateValue: the value with placeholders
	variable *envVariable    // variableValue
	// target is the resource a referenceValue refers to, written where
	// target is.
	target *stackResource
	// local is the named local a templateValue that is the resource's name
	// is written as, such as orders_name for local.orders_name; "" for
	// none.
	local string
}

// mergedBlock is a nested block of a merged body, or, for blocks some
// environments have more or fewer of, a dynamic block over variable: a
// list of objects, or, where single, an object or null; content sets
// fields from each.
type mergedBlock struct {
	typ      string
	body     *mergedBody // nil for a dynamic block
	variable *envVariable
	single   bool
	fields   []string
}

// forEachGroup is resources of one type and compatible shape the merged
// root writes as one resource with for_each over a variable of each
// environment's keys, the resources' labels, such as
// aws_sqs_queue.this["orders"]: so environments may have different
// numbers of them. Resources every environment has are one too, where
// there are minRepeated of them or more, with for_each over a local map
// where every environment's keys are the same (see keysLocal).
type forEachGroup struct {
	resourceType, label string
	keys                []*stackResource // by label
	// fields are the arguments that differ between the keys, written
	// each.value.<field>, and variable is the map of each environment's
	// keys to objects of them; nil where local is.
	fields   []string
	variable *envVariable
	body     *mergedBody
	// localKeys are, where every environment has every key with the same
	// fields, or the same once normalised, each key's fields, as keys
	// are, and local the name of the local map of them its for_each is
	// over (see render).
	localKeys []*mergedBody
	local     string
}

func (g *forEachGroup) address() string {
	return g.resourceType + "." + g.label
}

// source is what g's for_each is over: its local map or its variable.
func (g *forEachGroup) source() string {
	if g.variable == nil {
		return "local." + g.local
	}
	return "var." + g.variable.name
}

// minRepeated is the fewest resources of one shape, every environment
// having each, that become one resource with for_each.
const minRepeated = 3

// envVariable is a variable of the merged root and each environment's
// value of it.
type envVariable struct {
	name string
	// resourceType is the type of the resource it is for, which names it
	// where two names collide; "" for none.
	resourceType string
	what         string // what it holds, for its description
	// description, if set, is its description as it is, instead of
	// one of what.
	description string
	ty          cty.Type // cty.DynamicPseudoType if unknown
	// values are each environment's value, by environment, for the
	// environments that have what it is for: nil for null; where sensitive,
	// nil for each environment that sets it.
	values map[int]hclwrite.Tokens
	// sensitive marks a secret: no environment's tfvars sets it.
	sensitive    bool
	hasDefault   bool
	defaultValue hclwrite.Tokens // nil for null
}

// nullKey is valueKey's key for null.
const nullKey = "\x00null"

func valueKey(tokens hclwrite.Tokens) string {
	if tokens == nil {
		return nullKey
	}
	return exprKey(tokens)
}

// setDefault gives v a default where it should have one, of envs
// environments: with three or more, the value a strict majority of them
// have, null included, which those then leave out of their tfvars; else
// null, where an environment has it null or lacks what it is for. A
// sensitive variable, whose values aren't known, gets null only where an
// environment doesn't set it.
func (v *envVariable) setDefault(envs int) {
	optional := len(v.values) < envs
	if v.sensitive {
		// Its values are unknown: null only where an environment doesn't
		// set it.
		if optional {
			v.hasDefault, v.defaultValue = true, nil
		}
		return
	}
	for _, value := range v.values {
		if value == nil {
			optional = true
		}
	}
	if envs >= 3 {
		counts := map[string]int{}
		for _, value := range v.values {
			counts[valueKey(value)]++
		}
		for _, e := range slices.Sorted(maps.Keys(v.values)) {
			if value := v.values[e]; 2*counts[valueKey(value)] > envs {
				v.hasDefault, v.defaultValue = true, value
				return
			}
		}
	}
	if optional {
		v.hasDefault, v.defaultValue = true, nil
	}
}

// tfvars returns what environment env's terraform.tfvars sets v to, and
// false where it sets nothing: v is sensitive, env lacks what it is for,
// or the default holds env's value.
func (v *envVariable) tfvars(env int) (hclwrite.Tokens, bool) {
	value, ok := v.values[env]
	switch {
	case v.sensitive || !ok:
		return nil, false
	case v.hasDefault && valueKey(value) == valueKey(v.defaultValue):
		return nil, false
	case value == nil:
		return hclwrite.TokensForValue(cty.NullVal(cty.DynamicPseudoType)), true
	}
	return value, true
}

// mergeKey is the key of an environment's resource in mergeControls: the
// environment's index and the resource's address in its root.
func mergeKey(env int, address string) string {
	return fmt.Sprintf("%d\x00%s", env, address)
}

// mergeControls change how mergeStackWith merges, as the gate asks once
// a plan of the merged root fails (see MergeRoots).
type mergeControls struct {
	// split are resources not to merge with the other environments', by
	// mergeKey, with why: each stays residue of its environment.
	split map[string]string
	// isolated are resources matched with no other environment's, by
	// mergeKey: what an incremental import added since the stack was
	// merged (see MergeRoot.Isolated).
	isolated map[string]bool
	// labels are the labels of resources kept apart, by mergeKey, that
	// they had in the merged root before (see MergeRoot.Labels).
	labels map[string]string
}

// mergeStackWith models how roots, the roots of one stack, one per
// environment, of provider (aws, azurerm, google or google-beta), which
// profile describes, merge into one root: it matches their resources (see
// matchResourcesWith), leaves as residue each environment's part of those
// that can't merge (see mergeBody), puts resources of some environments
// only as keys of a compatible resource's for_each (see groupForEach) or
// in features counted by a deploy_ variable (see features), and names the
// variables, with their defaults (see envVariable.setDefault). Controls,
// if set, name the resources to split or keep apart (see mergeControls).
func mergeStackWith(provider string, profile MergeProfile, roots []envRoot, schemas *tfjson.ProviderSchemas, controls *mergeControls) *stackMerge {
	if controls == nil {
		controls = &mergeControls{}
	}
	m := &stackMerge{provider: provider, profile: profile, roots: roots, schemas: schemas, controls: controls}
	for _, root := range roots {
		m.envs = append(m.envs, root.env)
		n := newNormaliser(root)
		if profile.ScopeData == "" {
			// No data source gives the scope's ID.
			n.scope = ""
		}
		if profile.RegionArgument == "" {
			// The region isn't the provider's, which plans each
			// environment in its own.
			n.region = ""
		}
		m.norms = append(m.norms, n)
	}
	m.resources = matchResourcesWith(roots, m.norms, controls.isolated, controls.labels)
	for _, r := range m.resources {
		for _, member := range r.members {
			if member != nil && member.isolated {
				r.residue = "an incremental import added it since the stack was merged, so it stays its environment's"
			}
		}
	}
	m.index = make([]map[string]*envResource, len(roots))
	for e := range roots {
		m.index[e] = map[string]*envResource{}
	}
	for _, r := range m.resources {
		for e, member := range r.members {
			if member != nil {
				m.index[e][member.block.Address] = member
			}
		}
	}
	m.splitForced()
	m.splitResidue()
	m.groupForEach()
	m.build()
	return m
}

// splitForced splits each resource matched across environments that
// m.controls says to split (see mergeControls.split) into residue of
// each environment.
func (m *stackMerge) splitForced() {
	if len(m.controls.split) == 0 {
		return
	}
	for split := true; split; {
		split = false
		for _, r := range m.resources {
			if r.residue != "" {
				continue
			}
			for e, member := range r.members {
				if member == nil {
					continue
				}
				if reason, ok := m.controls.split[mergeKey(e, member.block.Address)]; ok {
					m.split(r, reason)
					split = true
					break
				}
			}
			if split {
				break
			}
		}
	}
}

// MergeProfile is what the root the environments of a stack share reads
// of each environment's account (or subscription, or project) and
// region, where the environments' roots differ by them. The importer
// fills it from the provider's profile (see harvest.Merge, and
// harvest.Scope's noun for ScopeTitle), so that the engine needn't tell
// providers apart.
type MergeProfile struct {
	// ScopeTitle is what the roots are scoped by, for the README, such as
	// Account.
	ScopeTitle string
	// ScopeData is the data source that reads the ID of the account,
	// ScopeLocal the local that holds the ID, and ScopeExpr the
	// expression that reads it; all empty for a provider with none.
	ScopeData, ScopeLocal, ScopeExpr string
	// RegionArgument is the argument of the provider block that sets the
	// region of the provider's resources, which differs between the
	// environments, such as AWS's region; empty for a provider whose
	// roots don't read their region from the provider, in which case the
	// other Region fields are empty too.
	RegionArgument string
	// RegionTitle names the region, for the variable that holds it, such
	// as AWS region.
	RegionTitle string
	// RegionData is the data source that reads the region, RegionLocal
	// the local that holds it, which also names the variable that sets
	// the provider's, and RegionExpr the expression that reads it.
	RegionData, RegionLocal, RegionExpr string
	// EnvironmentData returns what an environment's credentials and
	// region give the data sources that read its scope and region, for
	// UnmergeEnv.Data, by data source type and attribute, for the scope
	// ID and the region of an environment, "" where they aren't known;
	// nil for a provider with none.
	EnvironmentData func(scope, region string) map[string]map[string]string
}

// splitResidue splits each resource matched across environments whose
// bodies don't merge (see mergeBody) into one resource per environment,
// with why, until all that are left merge: one that refers to a split
// resource then refers to different resources in each environment.
func (m *stackMerge) splitResidue() {
	for split := true; split; {
		split = false
		for _, r := range m.resources {
			if r.coverage() < 2 {
				continue
			}
			if _, reason := m.mergeBody(r); reason != "" {
				m.split(r, reason)
				split = true
				break
			}
		}
	}
}

// split replaces r with a resource for each environment that has it,
// labelled with the environment's name after r's label.
func (m *stackMerge) split(r *stackResource, reason string) {
	m.resources = slices.DeleteFunc(m.resources, func(s *stackResource) bool { return s == r })
	used := m.labels()
	for e, member := range r.members {
		if member == nil {
			continue
		}
		part := &stackResource{resourceType: r.resourceType, rule: r.rule, members: make([]*envResource, len(r.members)), seq: r.seq, residue: reason}
		part.members[e] = member
		part.label = uniqueIn(used, r.resourceType, r.label+"_"+hclfile.Label(m.envs[e]))
		member.stack = part
		m.resources = append(m.resources, part)
	}
	sort.Slice(m.resources, func(i, j int) bool { return m.resources[i].address() < m.resources[j].address() })
}

// labels returns the addresses of the merged root's resources and forEach
// groups, as uniqueIn takes them.
func (m *stackMerge) labels() map[string]bool {
	used := map[string]bool{}
	for _, r := range m.resources {
		used[r.resourceType+"."+r.label] = true
	}
	for _, g := range m.forEach {
		used[g.address()] = true
	}
	return used
}

// groupForEach makes each resource that some environments only have,
// and isn't residue, a key of a forEach with the other resources of its
// type it is compatible with (see keysBody), if any; then each resource
// every environment has, where minRepeated of its type, every
// environment having each, are compatible. No key of a forEach refers to
// another, as a resource's instances can't refer to one another.
func (m *stackMerge) groupForEach() {
	byType := map[string][]*stackResource{}
	for _, r := range m.resources {
		if r.residue == "" {
			byType[r.resourceType] = append(byType[r.resourceType], r)
		}
	}
	used := m.labels()
	group := func(typ string, full bool) {
		candidates := byType[typ]
		for _, seed := range candidates {
			if seed.forEach != nil || (seed.coverage() == len(m.envs)) != full || typ == moduleType {
				continue
			}
			keys := []*stackResource{seed}
			for _, other := range candidates {
				if other == seed || other.forEach != nil || (full && other.coverage() < len(m.envs)) {
					continue
				}
				if slices.ContainsFunc(keys, func(k *stackResource) bool { return refersTo(k, other) || refersTo(other, k) }) {
					continue
				}
				if _, _, ok := m.keysBody(append(slices.Clone(keys), other)); ok {
					keys = append(keys, other)
				}
			}
			if len(keys) < 2 || (full && len(keys) < minRepeated) {
				continue
			}
			sort.Slice(keys, func(i, j int) bool { return keys[i].label < keys[j].label })
			g := &forEachGroup{resourceType: typ, label: uniqueIn(used, typ, "this"), keys: keys}
			for _, k := range keys {
				k.forEach = g
			}
			m.forEach = append(m.forEach, g)
		}
	}
	for _, full := range []bool{false, true} {
		for _, typ := range slices.Sorted(maps.Keys(byType)) {
			group(typ, full)
		}
	}
	sort.Slice(m.forEach, func(i, j int) bool { return m.forEach[i].address() < m.forEach[j].address() })
}

// build merges each forEach's body (see forEachBodies), gives the
// resources some environments lack, outside a forEach, their features'
// deploy variables (see features), merges each resource's body, merges
// the data sources, which a feature's deploy variable counts where only
// it reads one, and names and renders the variables.
func (m *stackMerge) build() {
	m.forEachBodies()
	m.features()
	for _, r := range m.resources {
		if r.forEach != nil {
			continue
		}
		// Merged before, with the same references; only how they are
		// written has changed.
		body, reason := m.mergeBody(r)
		if reason != "" {
			r.residue = reason
		}
		r.body = body
	}
	for _, g := range m.forEach {
		g.variable = m.keysVariable(g)
		if keys := m.keysLocal(g); keys != nil {
			g.variable, g.localKeys = nil, keys
		}
	}
	m.reason = m.mergeData()
	m.nameVariables()
	m.render()
	m.indexGatedData()
}

// forEachBodies merges each forEach's body. groupForEach made each of
// keys keysBody merged, with the same references, only written as they
// were then; should a group's keys no longer merge, the group is
// dissolved: its keys become resources of their own, before features
// gives those some environments lack their deploy variables. References
// to them are then written differently, so the other groups' bodies are
// merged again.
func (m *stackMerge) forEachBodies() {
	for {
		var kept []*forEachGroup
		for _, g := range m.forEach {
			body, fields, ok := m.keysBody(g.keys)
			if !ok {
				for _, k := range g.keys {
					k.forEach = nil
				}
				continue
			}
			g.body, g.fields = body, fields
			kept = append(kept, g)
		}
		if len(kept) == len(m.forEach) {
			return
		}
		m.forEach = kept
	}
}

// visit calls attr for each argument of b and its nested blocks, and
// block, if set, for each nested block.
func (b *mergedBody) visit(attr func(*mergedAttribute), block func(*mergedBlock)) {
	for _, a := range b.attributes {
		attr(a)
	}
	for _, blk := range b.blocks {
		if block != nil {
			block(blk)
		}
		if blk.body != nil {
			blk.body.visit(attr, block)
		}
	}
}
