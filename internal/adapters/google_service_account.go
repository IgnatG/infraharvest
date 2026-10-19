// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"slices"
	"strconv"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// GoogleServiceAccount maps a service account and the roles it has in its
// project onto the simple-sa module of
// terraform-google-modules/service-accounts/google: one account per call,
// so each keeps its own display name and description. It declines
// conditional grants, which the module can't make.
var GoogleServiceAccount = Adapter{
	Source:   "terraform-google-modules/service-accounts/google//modules/simple-sa",
	Version:  "5.1.0",
	Anchor:   "google_service_account",
	Members:  []string{"google_project_iam_member"},
	Repeated: []string{"google_project_iam_member"},
	Inputs:   []string{"description", "display_name", "name", "project_id", "project_roles"},
	Outputs:  []string{"email", "iam_email", "id"},
	Targets:  []string{"google_project_iam_member.roles", "google_service_account.sa"},
	Map:      mapGoogleServiceAccount,
	// The module sets these from the inputs as they are
	// (modules/simple-sa/main.tf).
	Inverse: map[string]string{
		"google_service_account.sa.description":  "description",
		"google_service_account.sa.display_name": "display_name",
	},
}

func mapGoogleServiceAccount(c Cluster) (*Call, error) {
	call := &Call{
		Addresses: map[string]string{c.Anchor.Address(): "google_service_account.sa"},
		Outputs: map[string]map[string]string{c.Anchor.Address(): {
			"email":  "email",
			"id":     "id",
			"member": "iam_email",
			"name":   "id", // projects/<project>/serviceAccounts/<email>, as id
		}},
	}
	sa := Read(c.Anchor.Address(), c.Anchor.Body)
	project, ok := sa.Attr("project")
	if !ok {
		return nil, Declinef("%s has no project", c.Anchor.Address())
	}
	call.Set("project_id", project)
	name, ok := sa.Attr("account_id")
	if !ok {
		return nil, Declinef("%s has no account ID", c.Anchor.Address())
	}
	call.Set("name", name)
	// The module's defaults are a display name of its own and no
	// description; the provider's, neither.
	for _, arg := range []string{"description", "display_name"} {
		value, ok := sa.Attr(arg)
		if !ok {
			value = hclwrite.TokensForValue(cty.StringVal(""))
		}
		call.Set(arg, value)
	}
	sa.IgnoreDefault("disabled", cty.False)
	// Terraform's own settings, which an import records at their defaults.
	sa.IgnoreDefault("create_ignore_already_exists", cty.False)
	sa.IgnoreDeletionPolicy()
	if err := sa.Done(); err != nil {
		return nil, err
	}

	var roles []string
	for _, m := range c.Members {
		r := Read(m.Address(), m.Body)
		if !r.RefersTo("member", c.Anchor) {
			return nil, Declinef("%s doesn't grant a role to %s", m.Address(), c.Anchor.Address())
		}
		if p, ok := r.Attr("project"); !ok || !sameTokens(p, project) {
			return nil, Declinef("%s grants a role in another project than %s's, and the module grants them in its own", m.Address(), c.Anchor.Address())
		}
		role, ok := literalString(mustAttr(r, "role"))
		if !ok {
			return nil, Declinef("%s has no literal role", m.Address())
		}
		if slices.Contains(roles, role) {
			return nil, Declinef("%s has the role %s twice", c.Anchor.Address(), role)
		}
		if err := r.Done(); err != nil {
			return nil, err
		}
		roles = append(roles, role)
		call.Addresses[m.Address()] = "google_project_iam_member.roles[" + strconv.Quote(role) + "]"
	}
	if len(roles) > 0 {
		slices.Sort(roles)
		call.Set("project_roles", hclwrite.TokensForValue(stringList(roles)))
	}
	return call, nil
}
