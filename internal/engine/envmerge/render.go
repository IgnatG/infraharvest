// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
)

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
		m.locals = append(m.locals, namedLocal{"naming_prefix", hclfile.ParseExpr(`"` + m.workload + `-${var.` + envVariableName + `}"`)})
	}
	if holds(templates, accountPlaceholder) {
		m.data = append(m.data, m.profile.ScopeData)
		m.locals = append(m.locals, namedLocal{m.profile.ScopeLocal, hclfile.ParseExpr(m.profile.ScopeExpr)})
	}
	if holds(templates, regionPlaceholder) {
		m.data = append(m.data, m.profile.RegionData)
		m.locals = append(m.locals, namedLocal{m.profile.RegionLocal, hclfile.ParseExpr(m.profile.RegionExpr)})
	}
	for _, b := range bodies {
		b.visit(func(a *mergedAttribute) {
			switch a.kind {
			case templateValue:
				a.value = m.renderTemplate(a.normal)
			case variableValue:
				a.value = hclwrite.TokensForTraversal(hcl.Traversal{hcl.TraverseRoot{Name: "var"}, hcl.TraverseAttr{Name: a.variable.name}})
			case referenceValue:
				a.value = hclfile.ParseExpr(string(m.presence(a.target).Bytes()) + " ? " + strings.TrimSpace(string(a.value.Bytes())) + " : null")
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
			a.local = uniqueEnvName(used, hclfile.CallName(r.resourceType+"."+r.label)+"_name", r.resourceType)
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

// presence is an expression that is true where the merged root has r:
// its deploy variable, or, for a key of a forEach, whether the forEach's
// variable has the key.
func (m *stackMerge) presence(r *stackResource) hclwrite.Tokens {
	if r.forEach != nil {
		return hclfile.ParseExpr(fmt.Sprintf("contains(keys(%s), %q)", r.forEach.source(), r.label))
	}
	return hclwrite.TokensForTraversal(hcl.Traversal{hcl.TraverseRoot{Name: "var"}, hcl.TraverseAttr{Name: r.deploy.name}})
}

// renderTemplate returns a template value's placeholders as the merged
// root reads them: <workload>-{env} as local.naming_prefix, {env} as
// var.env, the scope and region from their locals, and the resource
// group from its variable.
func (m *stackMerge) renderTemplate(normal hclwrite.Tokens) hclwrite.Tokens {
	placeholders := strings.NewReplacer(
		envPlaceholder, "${var."+envVariableName+"}",
		accountPlaceholder, "${local."+m.profile.ScopeLocal+"}",
		regionPlaceholder, "${local."+m.profile.RegionLocal+"}",
		resourceGroupPlaceholder, "${var."+resourceGroupVariableName+"}",
	)
	return mapLiterals(normal, func(s string) string {
		if m.workload != "" {
			s = withNamingPrefix(s, m.workload+"-"+envPlaceholder)
		}
		return placeholders.Replace(s)
	})
}
