// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package okta

import (
	"context"
	"net/url"

	"github.com/okta/okta-sdk-golang/v5/okta"
)

// oktaPolicy is what the policy listers need of a policy, whatever its type.
type oktaPolicy struct {
	ID   string
	Name string
}

// listPolicies returns every policy of policyType (PASSWORD, OKTA_SIGN_ON,
// MFA_ENROLL, ...).
func listPolicies(ctx context.Context, client *okta.APIClient, policyType string) ([]oktaPolicy, error) {
	policies, err := allPages(client.PolicyAPI.ListPolicies(ctx).Type_(policyType).Execute())
	if err != nil {
		return nil, err
	}
	return toOktaPolicies(policies), nil
}

// toOktaPolicies reads the common fields of each policy, whichever policy
// type schema it was decoded as.
func toOktaPolicies(items []okta.ListPolicies200ResponseInner) []oktaPolicy {
	var policies []oktaPolicy
	for i := range items {
		policy, ok := items[i].GetActualInstance().(interface {
			GetId() string
			GetName() string
		})
		if !ok {
			continue
		}
		policies = append(policies, oktaPolicy{ID: policy.GetId(), Name: policy.GetName()})
	}
	return policies
}

// oktaPolicyRule is what the policy rule listers need of a policy rule.
type oktaPolicyRule struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// listPolicyRules returns the rules of a policy. The SDK cannot decode the
// rules of an MFA_ENROLL policy (its rule union has no variant for them), so
// the rules are read as plain JSON, as the provider SDK used to.
func listPolicyRules(ctx context.Context, client *rawClient, policyID string) ([]oktaPolicyRule, error) {
	return rawList[oktaPolicyRule](ctx, client, "/api/v1/policies/"+url.PathEscape(policyID)+"/rules")
}
