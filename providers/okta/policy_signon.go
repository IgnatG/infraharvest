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
	"context"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/okta/okta-sdk-golang/v5/okta"
)

type SignOnPolicyGenerator struct {
	OktaService
}

func (g SignOnPolicyGenerator) createResources(signOnPolicyList []oktaPolicy) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, signOnPolicy := range signOnPolicyList {
		resourceName := normalizeResourceName(signOnPolicy.Name)
		resourceType := "okta_policy_signon"

		resources = append(resources, terraformutils.NewSimpleResource(
			signOnPolicy.ID,
			"policy_signon_"+resourceName,
			resourceType,
			"okta"))
	}
	return resources
}

func (g *SignOnPolicyGenerator) InitResources() error {
	ctx, client, e := g.Client()
	if e != nil {
		return e
	}

	output, err := getSignOnPolicies(ctx, client)
	if err != nil {
		return err
	}
	g.Resources = g.createResources(output)
	return nil
}

func getSignOnPolicies(ctx context.Context, client *okta.APIClient) ([]oktaPolicy, error) {
	return listPolicies(ctx, client, "OKTA_SIGN_ON")
}
