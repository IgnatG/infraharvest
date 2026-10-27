// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// googleProjectIAM maps the IAM grants in a project (google_project_iam_member)
// onto the projects_iam module of terraform-google-modules/iam/google, in
// its additive mode: one call per project, with the members of each role
// and the conditional grants. Grants to a service account the root has
// stay with the account (see googleServiceAccount, which is tried first),
// and grants with a role or member that isn't literal, such as a
// reference, stay in the root: the module's keys spell them out.
var googleProjectIAM = Adapter{
	Source:    "terraform-google-modules/iam/google//modules/projects_iam",
	Version:   "8.3.0",
	Anchor:    "google_project_iam_member",
	Members:   []string{"google_project_iam_member"},
	Repeated:  []string{"google_project_iam_member"},
	Group:     grantProject,
	Inputs:    []string{"bindings", "conditional_bindings", "projects"},
	Outputs:   []string{"members", "projects", "roles"},
	Providers: []string{"google"},
	Targets:   []string{"google_project_iam_member.project_iam_additive"},
	Map:       mapGoogleProjectIAM,
}

// grantProject groups a grant with a literal role and member by its
// project.
func grantProject(r Resource) (string, bool) {
	for _, name := range []string{"role", "member"} {
		attr := r.Body.GetAttribute(name)
		if attr == nil {
			return "", false
		}
		if _, ok := literalString(attr.Expr().BuildTokens(nil)); !ok {
			return "", false
		}
	}
	project := r.Body.GetAttribute("project")
	if project == nil {
		return "", false
	}
	return string(tokensKey(project.Expr().BuildTokens(nil))), true
}

// grantCondition is a conditional grant's condition. The module writes an
// empty description for one without.
type grantCondition struct {
	title, description, expression string
}

func mapGoogleProjectIAM(c Cluster) (*Call, error) {
	// The call holds the project's grants: named after one, such as its
	// owner's, it would read as if it held that grant alone.
	call := &Call{Name: "project_iam", Addresses: map[string]string{}}
	var project hclwrite.Tokens
	bindings := map[string][]string{}
	conditional := map[string]map[grantCondition][]string{}
	for _, g := range append([]Resource{c.Anchor}, c.Members...) {
		r := Read(g.Address(), g.Body)
		p, ok := r.Attr("project")
		if !ok {
			return nil, Declinef("%s has no project", g.Address())
		}
		if project == nil {
			project = p
		} else if !sameTokens(p, project) {
			return nil, Declinef("%s grants a role in another project than %s", g.Address(), c.Anchor.Address())
		}
		role, ok := literalString(mustAttr(r, "role"))
		if !ok {
			return nil, Declinef("%s has no literal role", g.Address())
		}
		member, ok := literalString(mustAttr(r, "member"))
		if !ok {
			return nil, Declinef("%s has no literal member", g.Address())
		}
		// The module's keys: the project, as "default" in a call with one,
		// the role, the condition's title and the member.
		key := "default--" + role + "--"
		cond, err := r.Block("condition")
		if err != nil {
			return nil, err
		}
		if cond == nil {
			if slices.Contains(bindings[role], member) {
				return nil, Declinef("%s grants %s to %s twice", c.Anchor.Address(), role, member)
			}
			bindings[role] = append(bindings[role], member)
		} else {
			gc, err := readCondition(g.Address(), cond)
			if err != nil {
				return nil, err
			}
			for other := range conditional[role] {
				if other.title == gc.title && other != gc {
					return nil, Declinef("%s has two conditions titled %q for %s, which the module keys by title", c.Anchor.Address(), gc.title, role)
				}
			}
			if conditional[role] == nil {
				conditional[role] = map[grantCondition][]string{}
			}
			if slices.Contains(conditional[role][gc], member) {
				return nil, Declinef("%s grants %s to %s twice on condition %q", c.Anchor.Address(), role, member, gc.title)
			}
			conditional[role][gc] = append(conditional[role][gc], member)
			key += gc.title + "--"
		}
		if err := r.Done(); err != nil {
			return nil, err
		}
		call.Addresses[g.Address()] = "google_project_iam_member.project_iam_additive[" + strconv.Quote(key+member) + "]"
	}

	call.Set("projects", hclwrite.TokensForTuple([]hclwrite.Tokens{project}))
	if len(bindings) > 0 {
		values := map[string]cty.Value{}
		for role, members := range bindings {
			slices.Sort(members)
			values[role] = stringList(members)
		}
		call.Set("bindings", hclwrite.TokensForValue(cty.MapVal(values)))
	}
	if len(conditional) > 0 {
		var entries []cty.Value
		for _, role := range slices.Sorted(maps.Keys(conditional)) {
			byTitle := func(a, b grantCondition) int { return strings.Compare(a.title, b.title) }
			for _, gc := range slices.SortedFunc(maps.Keys(conditional[role]), byTitle) {
				members := conditional[role][gc]
				slices.Sort(members)
				entries = append(entries, cty.ObjectVal(map[string]cty.Value{
					"role":        cty.StringVal(role),
					"title":       cty.StringVal(gc.title),
					"description": cty.StringVal(gc.description),
					"expression":  cty.StringVal(gc.expression),
					"members":     stringList(members),
				}))
			}
		}
		call.Set("conditional_bindings", hclwrite.TokensForValue(cty.TupleVal(entries)))
	}
	return call, nil
}

// readCondition reads a grant's condition block, which the module sets
// from literals.
func readCondition(address string, cond *Reader) (grantCondition, error) {
	var gc grantCondition
	var ok bool
	if gc.title, ok = literalString(mustAttr(cond, "title")); !ok {
		return gc, Declinef("%s has a condition without a literal title", address)
	}
	if gc.expression, ok = literalString(mustAttr(cond, "expression")); !ok {
		return gc, Declinef("%s has a condition without a literal expression", address)
	}
	if d, set := cond.Attr("description"); set {
		if gc.description, ok = literalString(d); !ok {
			return gc, Declinef("%s has a condition whose description isn't literal", address)
		}
	}
	return gc, nil
}
