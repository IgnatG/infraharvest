// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
)

// The variables a merged root always has, or has for its placeholders.
const (
	envVariableName           = "env"
	importIDsVariableName     = "import_ids"
	resourceGroupVariableName = "resource_group_name"
)

// stackMerge is how the roots of one stack, one per environment, become
// one root whose code every environment shares, with what differs in
// variables each environment sets.
type stackMerge struct {
	provider string
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

// mergeStack models how roots, the roots of one stack (see stackKey), one
// per environment, of provider (aws, azurerm, google or google-beta),
// merge into one root: it matches their resources (see matchResources),
// leaves as residue each environment's part of those that can't merge
// (see mergeBody), puts resources of some environments only as keys of a
// compatible resource's for_each (see groupForEach) or in features counted
// by a deploy_ variable (see features), and names the variables, with their defaults (see
// envVariable.setDefault).
func mergeStack(provider string, roots []envRoot, schemas *tfjson.ProviderSchemas) *stackMerge {
	return mergeStackWith(provider, roots, schemas, nil)
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
}

// mergeStackWith is mergeStack, with the resources controls name split
// or kept apart.
func mergeStackWith(provider string, roots []envRoot, schemas *tfjson.ProviderSchemas, controls *mergeControls) *stackMerge {
	if controls == nil {
		controls = &mergeControls{}
	}
	m := &stackMerge{provider: provider, roots: roots, schemas: schemas, controls: controls}
	dataSource, _, _ := scopeData(provider)
	for _, root := range roots {
		m.envs = append(m.envs, root.env)
		n := newNormaliser(root)
		if dataSource == "" {
			// No data source gives the scope's ID.
			n.scope = ""
		}
		if provider != "aws" {
			n.region = ""
		}
		m.norms = append(m.norms, n)
	}
	m.resources = matchResourcesWith(roots, m.norms, controls.isolated)
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
				m.index[e][member.block.address] = member
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
				if reason, ok := m.controls.split[mergeKey(e, member.block.address)]; ok {
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

// scopeData returns the data source that reads the ID of provider's
// account, subscription or project, the local that holds the ID, and the
// expression that reads it; "" for a provider with none.
func scopeData(provider string) (dataSource, local, expr string) {
	switch provider {
	case "aws":
		return "aws_caller_identity.current", "aws_account_id", "data.aws_caller_identity.current.account_id"
	case "azurerm":
		return "azurerm_client_config.current", "subscription_id", "data.azurerm_client_config.current.subscription_id"
	case "google", "google-beta":
		return "google_client_config.current", "project_id", "data.google_client_config.current.project"
	}
	return "", "", ""
}

// The data source and local of an AWS root's region.
const (
	regionData  = "aws_region.current"
	regionLocal = "aws_region"
)

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
		part.label = uniqueIn(used, r.resourceType, r.label+"_"+Label(m.envs[e]))
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

// build gives the resources some environments lack, outside a forEach,
// their features' deploy variables (see features), merges each
// resource's body and each forEach's, and names and renders the
// variables.
func (m *stackMerge) build() {
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
		g.body, g.fields, _ = m.keysBody(g.keys)
		g.variable = m.keysVariable(g)
		if keys := m.keysLocal(g); keys != nil {
			g.variable, g.localKeys = nil, keys
		}
	}
	m.reason = m.mergeData()
	m.nameVariables()
	m.render()
}

// features groups the resources some environments only have, outside a
// forEach, into features: resources the same environments have that
// refer to one another, or whose names start with the same word, such as
// aws_vpn_gateway.vpn and aws_vpn_connection.vpn_customer. One
// deploy_<feature> variable, true where an environment has the feature,
// counts every resource of it. A feature is named after the word most of
// its resources' names start with, or, for one resource, after the
// resource (see callName). Each residue resource is a feature of its own.
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
		name := callName(members[0].resourceType + "." + members[0].label)
		resourceType := members[0].resourceType
		if len(members) > 1 {
			name, resourceType = commonWord(members), ""
		}
		addresses := make([]string, len(members))
		for i, r := range members {
			addresses[i] = r.resourceType + "." + r.label
		}
		v := &envVariable{
			name:         "deploy_" + name,
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

// firstWord is the first word of r's name in the merged root (see
// callName).
func firstWord(r *stackResource) string {
	word, _, _ := strings.Cut(callName(r.resourceType+"."+r.label), "_")
	return word
}

// commonWord returns the first word (see firstWord) most of resources
// have; the first in order of a tie.
func commonWord(resources []*stackResource) string {
	counts := map[string]int{}
	for _, r := range resources {
		counts[firstWord(r)]++
	}
	best := ""
	for _, w := range slices.Sorted(maps.Keys(counts)) {
		if best == "" || counts[w] > counts[best] {
			best = w
		}
	}
	return best
}

// nameVariables collects the variables of the merged root, makes their
// names unique, and sets their defaults.
func (m *stackMerge) nameVariables() {
	env := &envVariable{name: envVariableName, what: "token that stands for the environment in names", ty: cty.String, values: map[int]hclwrite.Tokens{}}
	for e, n := range m.norms {
		env.values[e] = hclwrite.TokensForValue(cty.StringVal(n.token))
	}
	variables := []*envVariable{env}
	collect := func(b *mergedBody) {
		b.visit(func(a *mergedAttribute) {
			if a.variable != nil {
				variables = append(variables, a.variable)
			}
		}, func(blk *mergedBlock) {
			if blk.variable != nil {
				variables = append(variables, blk.variable)
			}
		})
	}
	deploys := map[*envVariable]bool{}
	for _, r := range m.resources {
		if r.deploy != nil && !deploys[r.deploy] {
			deploys[r.deploy] = true
			variables = append(variables, r.deploy)
		}
		if r.body != nil {
			collect(r.body)
		}
	}
	for _, g := range m.forEach {
		collect(g.body)
		if g.variable != nil {
			variables = append(variables, g.variable)
		}
	}
	for _, d := range m.rootData {
		collect(d.body)
	}
	used := map[string]bool{importIDsVariableName: true, resourceGroupVariableName: true}
	for _, v := range variables {
		v.name = uniqueEnvName(used, v.name, v.resourceType)
		if !v.hasDefault {
			v.setDefault(len(m.envs))
		}
	}
	m.variables = variables
}

// uniqueEnvName returns name, or where used has it, name after
// resourceType's name without its provider, or with _2, _3, ... after
// it, whichever first isn't in used; and records it there.
func uniqueEnvName(used map[string]bool, name, resourceType string) string {
	if _, rest, ok := strings.Cut(resourceType, "_"); ok && used[name] {
		name = rest + "_" + name
	}
	unique := name
	for n := 2; used[unique]; n++ {
		unique = fmt.Sprintf("%s_%d", name, n)
	}
	used[unique] = true
	return unique
}

// render writes the value of each argument that isn't written as it is,
// now that the variables have their names and local.naming_prefix its
// workload; and adds the locals, variables and data sources the values
// read. A template that is a resource's name (see isNameAttribute) and
// holds the environment's token becomes a named local, as does a residue
// resource's name (see residueName).
func (m *stackMerge) render() {
	var bodies []*mergedBody
	for _, r := range m.resources {
		if r.body != nil {
			bodies = append(bodies, r.body)
		}
	}
	for _, g := range m.forEach {
		bodies = append(bodies, g.body)
		bodies = append(bodies, g.localKeys...)
	}
	for _, d := range m.rootData {
		bodies = append(bodies, d.body)
	}
	var templates []hclwrite.Tokens
	for _, b := range bodies {
		b.visit(func(a *mergedAttribute) {
			if a.kind == templateValue {
				templates = append(templates, a.normal)
			}
		}, nil)
	}
	// The residue's names, by argument: templates where they hold their
	// environment's token, nil where they are kept as they are.
	residueNames := map[*mergedAttribute]hclwrite.Tokens{}
	for _, r := range m.resources {
		if a, normal := m.residueName(r); a != nil {
			residueNames[a] = normal
			if normal != nil {
				templates = append(templates, normal)
			}
		}
	}
	m.workload = workloadOf(templates)
	if m.workload != "" {
		m.locals = append(m.locals, namedLocal{"naming_prefix", exprTokens(`"` + m.workload + `-${var.` + envVariableName + `}"`)})
	}
	if holds(templates, accountPlaceholder) {
		dataSource, local, expr := scopeData(m.provider)
		m.data = append(m.data, dataSource)
		m.locals = append(m.locals, namedLocal{local, exprTokens(expr)})
	}
	if holds(templates, regionPlaceholder) {
		m.data = append(m.data, regionData)
		m.locals = append(m.locals, namedLocal{regionLocal, exprTokens("data." + regionData + ".region")})
	}
	for _, b := range bodies {
		b.visit(func(a *mergedAttribute) {
			switch a.kind {
			case templateValue:
				a.value = m.renderTemplate(a.normal)
			case variableValue:
				a.value = hclwrite.TokensForTraversal(hcl.Traversal{hcl.TraverseRoot{Name: "var"}, hcl.TraverseAttr{Name: a.variable.name}})
			case referenceValue:
				a.value = exprTokens(string(m.presence(a.target).Bytes()) + " ? " + strings.TrimSpace(string(a.value.Bytes())) + " : null")
			}
		}, nil)
	}
	used := map[string]bool{}
	for _, l := range m.locals {
		used[l.name] = true
	}
	for _, r := range m.resources {
		if r.body == nil {
			continue
		}
		for _, a := range r.body.attributes {
			value, residue := residueNames[a]
			switch {
			case residue && value != nil:
				value = m.renderTemplate(value)
			case residue:
				value = a.value
			case a.kind == templateValue && isNameAttribute(r.resourceType, a.name) && holds([]hclwrite.Tokens{a.normal}, envPlaceholder):
				value = a.value
			default:
				continue
			}
			a.local = uniqueEnvName(used, callName(r.resourceType+"."+r.label)+"_name", r.resourceType)
			m.locals = append(m.locals, namedLocal{a.local, value})
			a.value = hclwrite.TokensForTraversal(hcl.Traversal{hcl.TraverseRoot{Name: "local"}, hcl.TraverseAttr{Name: a.local}})
			break
		}
	}
	// Each local forEach's map, of its keys' fields.
	for _, g := range m.forEach {
		if g.localKeys == nil {
			continue
		}
		keys := make([]hclwrite.ObjectAttrTokens, len(g.keys))
		for i, k := range g.keys {
			var fields []hclwrite.ObjectAttrTokens
			for _, a := range g.localKeys[i].attributes {
				fields = append(fields, hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForIdentifier(a.name), Value: a.value})
			}
			keys[i] = hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForValue(cty.StringVal(k.label)), Value: hclwrite.TokensForObject(fields)}
		}
		g.local = uniqueEnvName(used, pluralName(g.resourceType), g.resourceType)
		m.locals = append(m.locals, namedLocal{g.local, hclwrite.TokensForObject(keys)})
	}
	if holds(templates, resourceGroupPlaceholder) {
		group := &envVariable{name: resourceGroupVariableName, what: "resource group of the environment", ty: cty.String, values: map[int]hclwrite.Tokens{}}
		for e, n := range m.norms {
			if n.group != "" {
				group.values[e] = hclwrite.TokensForValue(cty.StringVal(n.group))
			}
		}
		group.setDefault(len(m.envs))
		m.variables = append(m.variables, group)
	}
	sort.Slice(m.variables, func(i, j int) bool { return m.variables[i].name < m.variables[j].name })
}

// residueName returns the argument that is r's name, where r is residue
// and its name a literal string, and the name with envPlaceholder for its
// environment's tokens (see normaliser.envValue): a template its
// environment's var files give its value back from, as
// local.naming_prefix does. The template is nil where the name holds no
// token, or doesn't come back as it is, and the name stays as it is.
func (m *stackMerge) residueName(r *stackResource) (*mergedAttribute, hclwrite.Tokens) {
	if r.residue == "" || r.body == nil {
		return nil, nil
	}
	e := slices.IndexFunc(r.members, func(member *envResource) bool { return member != nil })
	if e < 0 {
		return nil, nil
	}
	n := m.norms[e]
	for _, a := range r.body.attributes {
		if a.kind != sameValue || !isNameAttribute(r.resourceType, a.name) {
			continue
		}
		if v, ok := literalValue(a.value); !ok || v.Type() != cty.String {
			continue
		}
		normal := mapLiterals(a.value, n.envValue)
		if !holds([]hclwrite.Tokens{normal}, envPlaceholder) || exprKey(mapLiterals(normal, n.denormalise)) != exprKey(a.value) {
			return a, nil
		}
		return a, normal
	}
	return nil, nil
}

// holds reports whether the string literals of any of templates hold
// placeholder.
func holds(templates []hclwrite.Tokens, placeholder string) bool {
	for _, tokens := range templates {
		for _, t := range tokens {
			if (t.Type == hclsyntax.TokenQuotedLit || t.Type == hclsyntax.TokenStringLit) && strings.Contains(string(t.Bytes), placeholder) {
				return true
			}
		}
	}
	return false
}

// isNameAttribute reports whether attr of resourceType is the resource's
// name: name, bucket or identifier, or <word>_name for a word of the
// type, as function_name of aws_lambda_function.
func isNameAttribute(resourceType, attr string) bool {
	switch attr {
	case "name", "bucket", "identifier":
		return true
	}
	word, ok := strings.CutSuffix(attr, "_name")
	_, rest, _ := strings.Cut(resourceType, "_")
	return ok && slices.Contains(strings.Split(rest, "_"), word)
}

// presence is an expression that is true where the merged root has r:
// its deploy variable, or, for a key of a forEach, whether the forEach's
// variable has the key.
func (m *stackMerge) presence(r *stackResource) hclwrite.Tokens {
	if r.forEach != nil {
		return exprTokens(fmt.Sprintf("contains(keys(%s), %q)", r.forEach.source(), r.label))
	}
	return hclwrite.TokensForTraversal(hcl.Traversal{hcl.TraverseRoot{Name: "var"}, hcl.TraverseAttr{Name: r.deploy.name}})
}

// renderTemplate returns a template value's placeholders as the merged
// root reads them: <workload>-{env} as local.naming_prefix, {env} as
// var.env, the scope and region from their locals, and the resource
// group from its variable.
func (m *stackMerge) renderTemplate(normal hclwrite.Tokens) hclwrite.Tokens {
	_, scope, _ := scopeData(m.provider)
	placeholders := strings.NewReplacer(
		envPlaceholder, "${var."+envVariableName+"}",
		accountPlaceholder, "${local."+scope+"}",
		regionPlaceholder, "${local."+regionLocal+"}",
		resourceGroupPlaceholder, "${var."+resourceGroupVariableName+"}",
	)
	return mapLiterals(normal, func(s string) string {
		if m.workload != "" {
			s = withNamingPrefix(s, m.workload+"-"+envPlaceholder)
		}
		return placeholders.Replace(s)
	})
}

// withNamingPrefix returns s with each occurrence of prefix, the naming
// prefix with placeholders, that no letter or digit comes right before
// made local.naming_prefix.
func withNamingPrefix(s, prefix string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, prefix)
		for i > 0 && isWordByte(s[i-1]) {
			next := strings.Index(s[i+1:], prefix)
			if next < 0 {
				i = -1
				break
			}
			i += 1 + next
		}
		if i < 0 {
			break
		}
		b.WriteString(s[:i])
		b.WriteString("${local.naming_prefix}")
		s = s[i+len(prefix):]
	}
	b.WriteString(s)
	return b.String()
}

// workloadOf returns what the names in templates, values with
// placeholders, most often have before -{env}: shop for shop-{env}-db and
// arn:aws:s3:::shop-{env}-logs. A tie goes to the shorter, then the first
// in order; "" for none.
func workloadOf(templates []hclwrite.Tokens) string {
	counts := map[string]int{}
	for _, tokens := range templates {
		for _, t := range tokens {
			if t.Type != hclsyntax.TokenQuotedLit && t.Type != hclsyntax.TokenStringLit {
				continue
			}
			s := string(t.Bytes)
			for from := 0; ; {
				i := strings.Index(s[from:], "-"+envPlaceholder)
				if i < 0 {
					break
				}
				i += from
				start := i
				for start > 0 && (isWordByte(s[start-1]) || s[start-1] == '-' || s[start-1] == '_') {
					start--
				}
				if w := strings.TrimLeft(s[start:i], "-_"); w != "" && !startsWithDigit(w) {
					counts[w]++
				}
				from = i + 1
			}
		}
	}
	best := ""
	for _, w := range slices.Sorted(maps.Keys(counts)) {
		if n := counts[w]; best == "" || n > counts[best] || (n == counts[best] && len(w) < len(best)) {
			best = w
		}
	}
	return best
}

// envBody is one environment's body of a resource, or of one of its
// nested blocks.
type envBody struct {
	env  int
	body *hclwrite.Body
}

// bodyMerger merges a resource's bodies across environments (see
// mergeBody), counting the arguments, and those that differ.
type bodyMerger struct {
	m                *stackMerge
	r                *stackResource
	total, differing int
	reason           string // why the bodies don't merge; "" if they do
}

func (b *bodyMerger) refuse(reason string) {
	if b.reason == "" {
		b.reason = reason
	}
}

// mergeBody merges the bodies of r's resources: arguments with the same
// value, once references are renamed to the merged root's resources, stay
// as they are; values the same once normalised become templates (see
// stackMerge.template); arguments set in some environments only, and
// other values that differ, variables (null where an environment doesn't
// set it); and nested blocks some environments have more or fewer of,
// dynamic blocks. It returns why the bodies don't merge where they
// don't: more than half of the arguments differ; a value that differs
// reads anything but variables, which only secrets do, and those become
// sensitive variables; a sensitive argument's literal value differs; or
// a dynamic block would hold blocks or references. A reference to a
// resource some environments lack is kept where it is set if every
// environment that has that resource sets it, and no other does.
func (m *stackMerge) mergeBody(r *stackResource) (*mergedBody, string) {
	var in []envBody
	for e, member := range r.members {
		if member != nil {
			in = append(in, envBody{e, member.block.write.Body()})
		}
	}
	b := &bodyMerger{m: m, r: r}
	body := b.body(in, nil, callName(r.resourceType+"."+r.label))
	switch {
	case b.reason != "":
		return nil, b.reason
	case 2*b.differing > b.total:
		return nil, fmt.Sprintf("%d of its %d arguments differ between the environments", b.differing, b.total)
	}
	return body, ""
}

// body merges bodies in, at path in the resource's schema; prefix
// starts the names of the variables it makes.
func (b *bodyMerger) body(in []envBody, path []string, prefix string) *mergedBody {
	out := &mergedBody{}
	for _, name := range attributeUnion(in) {
		if a := b.attribute(in, path, prefix, name); a != nil {
			out.attributes = append(out.attributes, a)
		}
	}
	for _, typ := range blockTypeUnion(in) {
		counts := make([]int, len(in))
		same := true
		for i, x := range in {
			counts[i] = len(blocksOf(x.body, typ))
			same = same && counts[i] == counts[0]
		}
		if !same {
			if blk := b.dynamic(in, path, prefix, typ); blk != nil {
				out.blocks = append(out.blocks, blk)
			}
			continue
		}
		for j := range counts[0] {
			inner := make([]envBody, len(in))
			for i, x := range in {
				inner[i] = envBody{x.env, blocksOf(x.body, typ)[j].Body()}
			}
			segment := typ
			if counts[0] > 1 {
				segment = fmt.Sprintf("%s_%d", typ, j)
			}
			out.blocks = append(out.blocks, &mergedBlock{typ: typ, body: b.body(inner, append(slices.Clone(path), typ), joinName(prefix, segment))})
		}
	}
	return out
}

func joinName(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "_" + name
}

// attribute merges the argument name of bodies in, or returns nil where
// they don't merge.
func (b *bodyMerger) attribute(in []envBody, path []string, prefix, name string) *mergedAttribute {
	values := make([]hclwrite.Tokens, len(in))
	original := make([]hclwrite.Tokens, len(in))
	present := 0
	for i, x := range in {
		if attr := x.body.GetAttribute(name); attr != nil {
			original[i] = attr.Expr().BuildTokens(nil)
			values[i] = b.m.renamed(x.env, original[i])
			present++
		}
	}
	b.total++
	if present == len(in) && sameExprs(values) {
		return &mergedAttribute{name: name, kind: sameValue, value: values[0]}
	}
	if b.r.resourceType == moduleType && len(path) == 0 && slices.Contains(moduleMetaArguments, name) {
		// Another module, or another release of it: the call is each
		// environment's own.
		b.refuse(fmt.Sprintf("its %s differs between the environments", name))
		return nil
	}
	if present == len(in) {
		if normal, ok := b.m.template(in, values); ok {
			return &mergedAttribute{name: name, kind: templateValue, normal: normal}
		}
	}
	b.differing++
	schemaPath := append(slices.Clone(path), name)
	at := strings.Join(schemaPath, ".")
	resources, variables, other := false, false, false
	for _, v := range values {
		if v != nil {
			res, vars, oth := exprReads(v)
			resources, variables, other = resources || res, variables || vars, other || oth
		}
	}
	switch {
	case resources:
		return b.reference(in, values, original, at)
	case other:
		b.refuse(fmt.Sprintf("its %s reads different values in different environments", at))
		return nil
	case !variables && b.m.sensitive(b.r.resourceType, schemaPath):
		b.refuse(fmt.Sprintf("its sensitive %s differs between the environments", at))
		return nil
	}
	if !variables && present == len(in) {
		if value, ok := b.m.envCondition(in, original); ok {
			return &mergedAttribute{name: name, kind: conditionValue, value: value}
		}
	}
	v := &envVariable{
		name:         joinName(prefix, name),
		resourceType: b.r.resourceType,
		what:         at + " of " + b.r.resourceType + "." + b.r.label,
		ty:           attributeTypeOrAny(b.m.schemas, b.r.resourceType, schemaPath),
		values:       map[int]hclwrite.Tokens{},
		sensitive:    variables,
	}
	for i, x := range in {
		switch {
		case !variables && original[i] != nil:
			// A var file, or the variable's default, holds no function
			// calls, such as the jsonencode of a policy.
			literal, ok := literalTokens(original[i])
			if !ok {
				b.refuse(fmt.Sprintf("its %s differs between the environments in a value a var file can't hold", at))
				return nil
			}
			v.values[x.env] = literal
		case !variables:
			v.values[x.env] = original[i]
		case original[i] != nil:
			// A secret: set, but not known.
			v.values[x.env] = nil
		}
	}
	return &mergedAttribute{name: name, kind: variableValue, variable: v}
}

// enumLike matches a string that looks like a value of an enum, such as
// ACTIVE, PAY_PER_REQUEST or Enabled, rather than a free setting.
var enumLike = regexp.MustCompile(`^([A-Z][A-Z0-9]*(_[A-Z0-9]+)*|[A-Z][a-z]+)$`)

// envCondition returns values, those of bodies in, as a condition on
// var.env, where they are literal booleans, or literal strings that look
// like an enum's (see enumLike), of two values, one of which one
// environment alone has: var.env == "<its token>" for true there and
// false elsewhere (!= for false there), else var.env == "<its token>" ?
// <its value> : <the others'>. Of two environments, that one is the one
// with true, for booleans, else the last.
func (m *stackMerge) envCondition(in []envBody, values []hclwrite.Tokens) (hclwrite.Tokens, bool) {
	if len(in) < 2 {
		return nil, false
	}
	literals := make([]cty.Value, len(values))
	for i, v := range values {
		lit, ok := literalValue(v)
		if !ok || (lit.Type() != cty.Bool && (lit.Type() != cty.String || !enumLike.MatchString(lit.AsString()))) {
			return nil, false
		}
		if i > 0 && !lit.Type().Equals(literals[0].Type()) {
			return nil, false
		}
		literals[i] = lit
	}
	counts := map[string]int{}
	for _, v := range values {
		counts[exprKey(v)]++
	}
	var alone []int
	for i, v := range values {
		if counts[exprKey(v)] == 1 {
			alone = append(alone, i)
		}
	}
	if len(counts) != 2 || len(alone) == 0 {
		return nil, false
	}
	odd := alone[len(alone)-1]
	isBool := literals[odd].Type() == cty.Bool
	if isBool && len(alone) == 2 && literals[alone[0]].True() {
		odd = alone[0]
	}
	other := 0
	if other == odd {
		other = 1
	}
	token := m.norms[in[odd].env].token
	if isBool {
		op := "=="
		if literals[odd].False() {
			op = "!="
		}
		return exprTokens(fmt.Sprintf("var.%s %s %q", envVariableName, op, token)), true
	}
	return exprTokens(fmt.Sprintf("var.%s == %q ? %s : %s", envVariableName, token, strings.TrimSpace(string(values[odd].Bytes())), strings.TrimSpace(string(values[other].Bytes())))), true
}

// literalValue returns the value of tokens, an expression, where it is a
// literal that isn't null.
func literalValue(tokens hclwrite.Tokens) (cty.Value, bool) {
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() || len(expr.Variables()) > 0 {
		return cty.NilVal, false
	}
	v, diags := expr.Value(nil)
	if diags.HasErrors() || !v.IsKnown() || v.IsNull() {
		return cty.NilVal, false
	}
	return v, true
}

// reference merges values, an argument at that refers to resources: the
// same where set, refusing it unless the environments that set it are
// those of in that have the resources.
func (b *bodyMerger) reference(in []envBody, values, original []hclwrite.Tokens, at string) *mergedAttribute {
	first := -1
	set := map[int]bool{}
	for i, v := range values {
		if v == nil {
			continue
		}
		switch {
		case first < 0:
			first = i
		case exprKey(v) != exprKey(values[first]):
			b.refuse(fmt.Sprintf("its %s refers to different resources in different environments", at))
			return nil
		}
		set[in[i].env] = true
	}
	targets := b.m.targets(in[first].env, original[first])
	if len(targets) == 0 {
		b.refuse(fmt.Sprintf("its %s refers to resources outside the stack", at))
		return nil
	}
	for _, t := range targets {
		for _, x := range in {
			if (t.members[x.env] != nil) != set[x.env] {
				b.refuse(fmt.Sprintf("its %s refers to %s, which some environments lack, and is set where they do", at, t.resourceType+"."+t.label))
				return nil
			}
		}
	}
	return &mergedAttribute{name: at[strings.LastIndexByte(at, '.')+1:], kind: referenceValue, value: values[first], target: targets[0]}
}

// dynamic merges the blocks of type typ of bodies in, which some have
// more or fewer of, into a dynamic block over a variable: an object, or
// null, for blocks no body has more than one of; else a list of objects.
// It returns nil where the blocks hold blocks or read anything, which the
// variable can't carry.
func (b *bodyMerger) dynamic(in []envBody, path []string, prefix, typ string) *mergedBlock {
	schemaPath := append(slices.Clone(path), typ)
	at := strings.Join(schemaPath, ".")
	most := 0
	names := map[string]bool{}
	for _, x := range in {
		blocks := blocksOf(x.body, typ)
		most = max(most, len(blocks))
		for _, blk := range blocks {
			if len(blk.Body().Blocks()) > 0 {
				b.refuse(fmt.Sprintf("its %s blocks differ between the environments and hold blocks", at))
				return nil
			}
			for name, attr := range blk.Body().Attributes() {
				names[name] = true
				if res, vars, other := exprReads(attr.Expr().BuildTokens(nil)); res || vars || other {
					b.refuse(fmt.Sprintf("its %s blocks differ between the environments and read values", at))
					return nil
				}
			}
		}
	}
	fields := slices.Sorted(maps.Keys(names))
	b.total += len(fields)
	b.differing += len(fields)
	types := map[string]cty.Type{}
	for _, f := range fields {
		types[f] = attributeTypeOrAny(b.m.schemas, b.r.resourceType, append(slices.Clone(schemaPath), f))
	}
	ty := cty.Object(types)
	single := most <= 1
	if !single {
		ty = cty.List(ty)
	}
	v := &envVariable{
		name:         joinName(prefix, typ),
		resourceType: b.r.resourceType,
		what:         at + " blocks of " + b.r.resourceType + "." + b.r.label,
		ty:           ty,
		values:       map[int]hclwrite.Tokens{},
	}
	for _, x := range in {
		blocks := blocksOf(x.body, typ)
		objects := make([]hclwrite.Tokens, len(blocks))
		for i, blk := range blocks {
			objects[i] = objectTokens(blk.Body())
		}
		switch {
		case single && len(objects) == 0:
			v.values[x.env] = nil
		case single:
			v.values[x.env] = objects[0]
		default:
			v.values[x.env] = hclwrite.TokensForTuple(objects)
		}
	}
	return &mergedBlock{typ: typ, variable: v, single: single, fields: fields}
}

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
				in = append(in, envBody{e, member.block.write.Body()})
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
		types[f] = attributeTypeOrAny(m.schemas, g.resourceType, []string{f})
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
				if attr := member.block.write.Body().GetAttribute(f); attr != nil {
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
				attr := member.block.write.Body().GetAttribute(f)
				if attr == nil {
					return nil
				}
				in = append(in, envBody{e, member.block.write.Body()})
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

// pluralName is the name of a forEach's variable: its resource type
// without the provider, in the plural, as sqs_queues for aws_sqs_queue.
func pluralName(resourceType string) string {
	_, name, ok := strings.Cut(resourceType, "_")
	if !ok {
		name = resourceType
	}
	switch {
	case strings.HasSuffix(name, "s"), strings.HasSuffix(name, "x"), strings.HasSuffix(name, "sh"), strings.HasSuffix(name, "ch"):
		return name + "es"
	case strings.HasSuffix(name, "y") && len(name) > 1 && !strings.ContainsRune("aeiou", rune(name[len(name)-2])):
		return name[:len(name)-1] + "ies"
	}
	return name + "s"
}

// blocksKey is a key of the nested blocks of body, in environment env,
// with references renamed: the same for the same blocks.
func (m *stackMerge) blocksKey(env int, body *hclwrite.Body) string {
	var b strings.Builder
	for _, blk := range body.Blocks() {
		b.WriteString(blk.Type())
		b.WriteString("{")
		for _, name := range slices.Sorted(maps.Keys(blk.Body().Attributes())) {
			fmt.Fprintf(&b, "%s=%s;", name, exprKey(m.renamed(env, blk.Body().GetAttribute(name).Expr().BuildTokens(nil))))
		}
		b.WriteString(m.blocksKey(env, blk.Body()))
		b.WriteString("}")
	}
	return b.String()
}

// constantBody is body, in environment env, as a merged body whose
// values are written as they are, with references renamed.
func (m *stackMerge) constantBody(env int, body *hclwrite.Body) *mergedBody {
	out := &mergedBody{}
	for _, name := range slices.Sorted(maps.Keys(body.Attributes())) {
		out.attributes = append(out.attributes, &mergedAttribute{name: name, kind: sameValue, value: m.renamed(env, body.GetAttribute(name).Expr().BuildTokens(nil))})
	}
	for _, blk := range body.Blocks() {
		out.blocks = append(out.blocks, &mergedBlock{typ: blk.Type(), body: m.constantBody(env, blk.Body())})
	}
	return out
}

// template returns values, those of bodies in, with placeholders (see
// normaliser.value), and true, where that makes them the same and each
// environment's value is the template with its own token, scope and
// group.
func (m *stackMerge) template(in []envBody, values []hclwrite.Tokens) (hclwrite.Tokens, bool) {
	normal := mapLiterals(values[0], m.norms[in[0].env].value)
	for i, x := range in {
		if exprKey(mapLiterals(values[i], m.norms[x.env].value)) != exprKey(normal) {
			return nil, false
		}
		if exprKey(mapLiterals(normal, m.norms[x.env].denormalise)) != exprKey(values[i]) {
			return nil, false
		}
	}
	return normal, true
}

// renamed returns tokens, an expression of environment env, with each
// reference to a resource of env's root made to the merged root's
// instance of it (see stackResource.instance).
func (m *stackMerge) renamed(env int, tokens hclwrite.Tokens) hclwrite.Tokens {
	return renameTokens(tokens, func(address string) (hclwrite.Tokens, bool) {
		r, ok := m.index[env][address]
		if !ok || r.stack == nil {
			return nil, false
		}
		return hclwrite.TokensForTraversal(r.stack.instance()), true
	})
}

// targets returns the merged root's resources tokens, an expression of
// environment env, refers to.
func (m *stackMerge) targets(env int, tokens hclwrite.Tokens) []*stackResource {
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() {
		return nil
	}
	var targets []*stackResource
	for _, t := range expr.Variables() {
		if r, ok := m.index[env][stackAddress(t)]; ok && r.stack != nil {
			targets = append(targets, r.stack)
		}
	}
	return targets
}

// sensitive reports whether the provider marks the attribute at path of
// resourceType sensitive.
func (m *stackMerge) sensitive(resourceType string, path []string) bool {
	attr := schemaAttribute(m.schemas, resourceType, path)
	return attr != nil && attr.Sensitive
}

// tfvars returns the variables environment env's terraform.tfvars sets,
// by name (see envVariable.tfvars).
func (m *stackMerge) tfvars(env int) map[string]hclwrite.Tokens {
	values := map[string]hclwrite.Tokens{}
	for _, v := range m.variables {
		if value, ok := v.tfvars(env); ok {
			values[v.name] = value
		}
	}
	return values
}

// imports returns the import IDs of environment env's resources, by the
// address of their instance in the merged root, which var.import_ids
// gives import blocks.
func (m *stackMerge) imports(env int) map[string]string {
	ids := map[string]string{}
	for _, r := range m.resources {
		if member := r.members[env]; member != nil && member.id != "" {
			ids[string(hclwrite.TokensForTraversal(r.instance()).Bytes())] = member.id
		}
		if member := r.members[env]; member != nil && r.resourceType == moduleType {
			call := addressText(r.instance())
			for rest, id := range member.moduleIDs {
				ids[call+"."+rest] = id
			}
		}
	}
	return ids
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

// attributeUnion returns the names of the arguments of bodies in, sorted.
func attributeUnion(in []envBody) []string {
	names := map[string]bool{}
	for _, x := range in {
		for name := range x.body.Attributes() {
			names[name] = true
		}
	}
	return slices.Sorted(maps.Keys(names))
}

// blockTypeUnion returns the types of the nested blocks of bodies in, in
// the order they first come.
func blockTypeUnion(in []envBody) []string {
	var types []string
	for _, x := range in {
		for _, blk := range x.body.Blocks() {
			if !slices.Contains(types, blk.Type()) {
				types = append(types, blk.Type())
			}
		}
	}
	return types
}

func blocksOf(body *hclwrite.Body, typ string) []*hclwrite.Block {
	var blocks []*hclwrite.Block
	for _, blk := range body.Blocks() {
		if blk.Type() == typ {
			blocks = append(blocks, blk)
		}
	}
	return blocks
}

// objectTokens is body's arguments as an object.
func objectTokens(body *hclwrite.Body) hclwrite.Tokens {
	var attrs []hclwrite.ObjectAttrTokens
	for _, name := range slices.Sorted(maps.Keys(body.Attributes())) {
		attrs = append(attrs, hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForIdentifier(name), Value: body.GetAttribute(name).Expr().BuildTokens(nil)})
	}
	return hclwrite.TokensForObject(attrs)
}

// exprKey is a key of tokens, an expression: the same for the same
// tokens, whatever the spaces between them.
func exprKey(tokens hclwrite.Tokens) string {
	var b strings.Builder
	for _, t := range tokens {
		if t.Type == hclsyntax.TokenComment {
			continue
		}
		b.Write(t.Bytes)
		b.WriteByte(0)
	}
	return b.String()
}

// sameExprs reports whether every one of values is set, and the same.
func sameExprs(values []hclwrite.Tokens) bool {
	for _, v := range values {
		if v == nil || exprKey(v) != exprKey(values[0]) {
			return false
		}
	}
	return true
}

// exprReads reports what tokens, an expression, refers to: resources,
// variables, and anything else, such as locals and data sources. An
// expression that doesn't parse reads something else.
func exprReads(tokens hclwrite.Tokens) (resources, variables, other bool) {
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() {
		return false, false, true
	}
	for _, t := range expr.Variables() {
		switch {
		case stackAddress(t) != "":
			resources = true
		case t.RootName() == "var":
			variables = true
		default:
			other = true
		}
	}
	return resources, variables, other
}

// literalTokens returns tokens, a value that reads nothing, as a var file
// or a variable's default can hold it: as it is without function calls,
// else evaluated, as jsonencode({...}) becomes the JSON string it gives;
// false where it can't be evaluated.
func literalTokens(tokens hclwrite.Tokens) (hclwrite.Tokens, bool) {
	expr, diags := hclsyntax.ParseExpression(tokens.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, false
	}
	calls := false
	_ = hclsyntax.VisitAll(expr, func(n hclsyntax.Node) hcl.Diagnostics {
		if _, ok := n.(*hclsyntax.FunctionCallExpr); ok {
			calls = true
		}
		return nil
	})
	if !calls {
		return tokens, true
	}
	v, diags := expr.Value(omitEvalContext)
	if diags.HasErrors() || !v.IsWhollyKnown() {
		return nil, false
	}
	return hclwrite.TokensForValue(v), true
}

// exprTokens parses src, an expression, into tokens; nil if it doesn't
// parse.
func exprTokens(src string) hclwrite.Tokens {
	f, diags := hclwrite.ParseConfig([]byte("x = "+src+"\n"), "", hcl.InitialPos)
	if diags.HasErrors() {
		return nil
	}
	return f.Body().GetAttribute("x").Expr().BuildTokens(nil)
}

// renameTokens returns tokens with each reference to a resource rename
// knows, by its address, made to what rename gives instead.
func renameTokens(tokens hclwrite.Tokens, rename func(string) (hclwrite.Tokens, bool)) hclwrite.Tokens {
	out := make(hclwrite.Tokens, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if t.Type == hclsyntax.TokenIdent && i+2 < len(tokens) && tokens[i+1].Type == hclsyntax.TokenDot && tokens[i+2].Type == hclsyntax.TokenIdent && (i == 0 || tokens[i-1].Type != hclsyntax.TokenDot) {
			if to, ok := rename(string(t.Bytes) + "." + string(tokens[i+2].Bytes)); ok && len(to) > 0 {
				for j, r := range to {
					c := *r
					if j == 0 {
						c.SpacesBefore = t.SpacesBefore
					}
					out = append(out, &c)
				}
				i += 2
				continue
			}
		}
		out = append(out, t)
	}
	return out
}
