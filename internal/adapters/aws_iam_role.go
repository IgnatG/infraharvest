// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// IAMRole maps a role, its inline policy, its policy attachments and its
// instance profile onto the iam-role module of
// terraform-aws-modules/iam/aws. The module names the inline policy and
// the instance profile after the role, and gives the profile the role's
// path and tags: it declines roles whose resources differ. The module
// sets force_detach_policies, which the provider keeps only in state.
var IAMRole = Adapter{
	Source:   "terraform-aws-modules/iam/aws//modules/iam-role",
	Version:  "6.8.2",
	Anchor:   "aws_iam_role",
	Members:  []string{"aws_iam_instance_profile", "aws_iam_role_policy", "aws_iam_role_policy_attachment"},
	Repeated: []string{"aws_iam_role_policy_attachment"},
	Inputs: []string{
		"create_inline_policy", "create_instance_profile", "description", "max_session_duration", "name",
		"path", "permissions_boundary", "policies", "source_inline_policy_documents",
		"source_trust_policy_documents", "tags", "use_name_prefix",
	},
	Outputs: []string{"arn", "instance_profile_arn", "instance_profile_id", "instance_profile_name", "instance_profile_unique_id", "name", "unique_id"},
	Map:     mapIAMRole,
}

func mapIAMRole(c Cluster) (*Call, error) {
	call := &Call{
		Addresses: map[string]string{c.Anchor.Address(): "aws_iam_role.this[0]"},
		Outputs: map[string]map[string]string{c.Anchor.Address(): {
			"arn":       "arn",
			"id":        "name", // a role's ID is its name
			"name":      "name",
			"unique_id": "unique_id",
		}},
	}
	role := Read(c.Anchor.Address(), c.Anchor.Body)
	if _, ok := role.Attr("name_prefix"); ok {
		return nil, Declinef("%s has a name prefix", c.Anchor.Address())
	}
	name, ok := role.Attr("name")
	if !ok {
		return nil, Declinef("%s has no name", c.Anchor.Address())
	}
	call.Set("name", name)
	call.Set("use_name_prefix", hclwrite.TokensForValue(cty.False))
	trust, ok := role.Attr("assume_role_policy")
	if !ok {
		return nil, Declinef("%s has no trust policy", c.Anchor.Address())
	}
	call.Set("source_trust_policy_documents", hclwrite.TokensForTuple([]hclwrite.Tokens{trust}))
	path, hasPath := role.Attr("path")
	for _, arg := range []string{"description", "max_session_duration", "path", "permissions_boundary"} {
		if value, ok := role.Attr(arg); ok {
			call.Set(arg, value)
		}
	}
	// Untagged as in the root, where the provider's default tags apply.
	tags, hasTags := role.Attr("tags")
	if hasTags {
		call.Set("tags", tags)
	} else {
		call.Set("tags", hclwrite.TokensForValue(cty.NullVal(cty.Map(cty.String))))
	}
	// The module always sets it; the provider keeps it only in state.
	role.Ignore("force_detach_policies")
	if err := role.Done(); err != nil {
		return nil, err
	}

	policies := map[string]hclwrite.Tokens{}
	var keys []string
	for _, m := range c.Members {
		r := Read(m.Address(), m.Body)
		if !r.RefersTo("role", c.Anchor) {
			return nil, Declinef("%s doesn't refer to %s", m.Address(), c.Anchor.Address())
		}
		switch m.Type {
		case "aws_iam_role_policy":
			if err := namedAfterRole(r, m, name); err != nil {
				return nil, err
			}
			policy, ok := r.Attr("policy")
			if !ok {
				return nil, Declinef("%s has no policy", m.Address())
			}
			call.Set("create_inline_policy", hclwrite.TokensForValue(cty.True))
			call.Set("source_inline_policy_documents", hclwrite.TokensForTuple([]hclwrite.Tokens{policy}))
			call.Addresses[m.Address()] = "aws_iam_role_policy.inline[0]"
		case "aws_iam_instance_profile":
			if err := namedAfterRole(r, m, name); err != nil {
				return nil, err
			}
			if err := sameAsRole(r, m, "path", path, hasPath); err != nil {
				return nil, err
			}
			if err := sameAsRole(r, m, "tags", tags, hasTags); err != nil {
				return nil, err
			}
			call.Set("create_instance_profile", hclwrite.TokensForValue(cty.True))
			call.Addresses[m.Address()] = "aws_iam_instance_profile.this[0]"
			call.Outputs[m.Address()] = map[string]string{
				"arn":       "instance_profile_arn",
				"id":        "instance_profile_id",
				"name":      "instance_profile_name",
				"unique_id": "instance_profile_unique_id",
			}
		case "aws_iam_role_policy_attachment":
			arn, ok := r.Attr("policy_arn")
			if !ok {
				return nil, Declinef("%s has no policy_arn", m.Address())
			}
			key := policyKey(arn, m.Name)
			if _, taken := policies[key]; taken {
				return nil, Declinef("%s attaches two policies named %s", c.Anchor.Address(), key)
			}
			policies[key] = arn
			keys = append(keys, key)
			call.Addresses[m.Address()] = "aws_iam_role_policy_attachment.this[" + strconv.Quote(key) + "]"
		}
		if err := r.Done(); err != nil {
			return nil, err
		}
	}
	if len(keys) > 0 {
		attrs := make([]hclwrite.ObjectAttrTokens, 0, len(keys))
		for _, key := range keys {
			attrs = append(attrs, hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForValue(cty.StringVal(key)), Value: policies[key]})
		}
		call.Set("policies", hclwrite.TokensForObject(attrs))
	}
	return call, nil
}

// namedAfterRole declines a member the module would name after the role
// unless it is.
func namedAfterRole(r *Reader, m Resource, role hclwrite.Tokens) error {
	if _, ok := r.Attr("name_prefix"); ok {
		return Declinef("%s has a name prefix", m.Address())
	}
	name, ok := r.Attr("name")
	if !ok || !sameTokens(name, role) {
		return Declinef("%s isn't named after the role, as the module names it", m.Address())
	}
	return nil
}

// sameAsRole declines a member whose argument differs from the role's,
// which the module sets for both.
func sameAsRole(r *Reader, m Resource, arg string, role hclwrite.Tokens, roleHas bool) error {
	value, ok := r.Attr(arg)
	if ok != roleHas || (ok && !sameTokens(value, role)) {
		return Declinef("%s has another %s than the role, as the module sets it", m.Address(), arg)
	}
	return nil
}

func sameTokens(a, b hclwrite.Tokens) bool {
	return bytes.Equal(bytes.Join(bytes.Fields(a.Bytes()), nil), bytes.Join(bytes.Fields(b.Bytes()), nil))
}

// policyKey names an attached policy in the module's policies map: the
// policy's name, from its ARN or the policy resource it refers to, else
// the attachment's own name.
func policyKey(arn hclwrite.Tokens, fallback string) string {
	expr, diags := hclsyntax.ParseExpression(arn.Bytes(), "", hcl.InitialPos)
	if diags.HasErrors() {
		return fallback
	}
	if v, diags := expr.Value(nil); !diags.HasErrors() && v.Type() == cty.String && v.IsKnown() && !v.IsNull() {
		s := v.AsString()
		return s[strings.LastIndexAny(s, "/:")+1:]
	}
	if t, ok := expr.(*hclsyntax.ScopeTraversalExpr); ok && len(t.Traversal) >= 2 && t.Traversal.RootName() == "aws_iam_policy" {
		if step, ok := t.Traversal[1].(hcl.TraverseAttr); ok {
			return step.Name
		}
	}
	return fallback
}
