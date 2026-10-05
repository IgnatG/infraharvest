// Copyright 2019 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package okta

import (
	"github.com/IgnatG/infraharvest/terraformutils"
)

type PasswordPolicyRuleGenerator struct {
	OktaService
}

func (g PasswordPolicyRuleGenerator) createResources(passwordPolicyRuleList []oktaPolicyRule, policyID string, policyName string) []terraformutils.Resource {
	var resources []terraformutils.Resource

	for _, policyRule := range passwordPolicyRuleList {
		resources = append(resources, terraformutils.NewResource(
			policyRule.ID,
			"policyrule_password_"+normalizeResourceName(policyName+"_"+policyRule.Name),
			"okta_policy_rule_password",
			"okta",
			map[string]string{
				"policy_id": policyID,
			}))
	}

	return resources
}

func (g *PasswordPolicyRuleGenerator) InitResources() error {
	var resources []terraformutils.Resource

	ctx, client, e := g.Client()
	if e != nil {
		return e
	}
	raw, err := newRawClient(client)
	if err != nil {
		return err
	}

	passwordPolicies, err := getPasswordPolicies(ctx, client)
	if err != nil {
		return err
	}

	for _, policy := range passwordPolicies {
		output, err := listPolicyRules(ctx, raw, policy.ID)
		if err != nil {
			return err
		}

		resources = append(resources, g.createResources(output, policy.ID, policy.Name)...)
	}

	g.Resources = resources
	return nil
}
