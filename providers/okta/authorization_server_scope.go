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
	"github.com/okta/okta-sdk-golang/v5/okta"
)

type AuthorizationServerScopeGenerator struct {
	OktaService
}

func (g AuthorizationServerScopeGenerator) createResources(authorizationServerScopeList []okta.OAuth2Scope, authorizationServerID string, authorizationServerName string) []terraformutils.Resource {
	var resources []terraformutils.Resource

	for _, authorizationServerScope := range authorizationServerScopeList {
		resources = append(resources, terraformutils.NewResource(
			authorizationServerScope.GetId(),
			normalizeResourceName("auth_server_"+authorizationServerName+"_scope_"+authorizationServerScope.GetName()),
			"okta_auth_server_scope",
			"okta",
			map[string]string{
				"auth_server_id": authorizationServerID,
			}))
	}
	return resources
}

func (g *AuthorizationServerScopeGenerator) InitResources() error {
	var resources []terraformutils.Resource
	ctx, client, e := g.Client()
	if e != nil {
		return e
	}

	authorizationServers, err := getAuthorizationServers(ctx, client)
	if err != nil {
		return err
	}

	for _, authorizationServer := range authorizationServers {
		output, err := allPages(client.AuthorizationServerScopesAPI.ListOAuth2Scopes(ctx, authorizationServer.GetId()).Execute())
		if err != nil {
			return err
		}

		resources = append(resources, g.createResources(output, authorizationServer.GetId(), authorizationServer.GetName())...)
	}

	g.Resources = resources
	return nil
}
