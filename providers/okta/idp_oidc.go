// Copyright 2021 The Terraformer Authors.
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

type IdpOIDCGenerator struct {
	OktaService
}

func (g IdpOIDCGenerator) createResources(idpOIDCList []okta.IdentityProvider) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, idp := range idpOIDCList {
		resources = append(resources, terraformutils.NewSimpleResource(
			idp.GetId(),
			"idp_"+normalizeResourceName(idp.GetType()+"_"+idp.GetName()),
			"okta_idp_oidc",
			"okta"))

	}
	return resources
}

func (g *IdpOIDCGenerator) InitResources() error {
	ctx, client, err := g.Client()
	if err != nil {
		return err
	}

	identityProviders, err := getIdpOIDC(ctx, client)
	if err != nil {
		return err
	}

	g.Resources = g.createResources(identityProviders)
	return nil
}

func getIdpOIDC(ctx context.Context, client *okta.APIClient) ([]okta.IdentityProvider, error) {
	return listIdentityProviders(ctx, client, "OIDC")
}

// listIdentityProviders returns every identity provider of idpType.
func listIdentityProviders(ctx context.Context, client *okta.APIClient, idpType string) ([]okta.IdentityProvider, error) {
	return allPages(client.IdentityProviderAPI.ListIdentityProviders(ctx).Type_(idpType).Execute())
}
