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

package azure

import (
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v11"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type ApplicationGatewayGenerator struct {
	AzureService
}

func (g ApplicationGatewayGenerator) createResources(applicationGateways []*armnetwork.ApplicationGateway) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, applicationGateway := range applicationGateways {
		resources = append(resources, terraformutils.NewSimpleResource(
			*applicationGateway.ID,
			*applicationGateway.Name,
			"azurerm_application_gateway",
			g.ProviderName))
	}
	return resources
}

func (g *ApplicationGatewayGenerator) InitResources() error {
	ctx := g.Context()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	applicationGatewaysClient, err := armnetwork.NewApplicationGatewaysClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}

	var applicationGateways []*armnetwork.ApplicationGateway
	if resourceGroup != "" {
		applicationGateways, err = listAll(ctx, applicationGatewaysClient.NewListPager(resourceGroup, nil),
			func(p armnetwork.ApplicationGatewaysClientListResponse) []*armnetwork.ApplicationGateway {
				return p.Value
			})
	} else {
		applicationGateways, err = listAll(ctx, applicationGatewaysClient.NewListAllPager(nil),
			func(p armnetwork.ApplicationGatewaysClientListAllResponse) []*armnetwork.ApplicationGateway {
				return p.Value
			})
	}
	g.Resources = g.createResources(applicationGateways)
	return err
}
