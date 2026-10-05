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

type AuthorizationServerGenerator struct {
	OktaService
}

func (g AuthorizationServerGenerator) createResources(authorizationServerList []okta.AuthorizationServer) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, authorizationServer := range authorizationServerList {
		resourceType := "okta_auth_server"
		if authorizationServer.GetName() == "default" {
			resourceType = "okta_auth_server_default"
		}

		resources = append(resources, terraformutils.NewSimpleResource(
			authorizationServer.GetId(),
			"auth_server_"+authorizationServer.GetName(),
			resourceType,
			"okta"))
	}
	return resources
}

func (g *AuthorizationServerGenerator) InitResources() error {
	ctx, client, e := g.Client()
	if e != nil {
		return e
	}

	output, err := getAuthorizationServers(ctx, client)
	if err != nil {
		return err
	}

	g.Resources = g.createResources(output)
	return nil
}

func getAuthorizationServers(ctx context.Context, client *okta.APIClient) ([]okta.AuthorizationServer, error) {
	return allPages(client.AuthorizationServerAPI.ListAuthorizationServers(ctx).Execute())
}

// authorizationServerPolicyIDName returns the ID and name of an authorization
// server policy. The SDK's AuthorizationServerPolicy model only declares the
// conditions, so the rest of the policy is in AdditionalProperties.
func authorizationServerPolicyIDName(policy okta.AuthorizationServerPolicy) (id, name string) {
	id, _ = policy.AdditionalProperties["id"].(string)
	name, _ = policy.AdditionalProperties["name"].(string)
	return id, name
}
