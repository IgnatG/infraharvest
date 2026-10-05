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
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v11"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type VirtualNetworkGenerator struct {
	AzureService
}

func (g VirtualNetworkGenerator) createResources(virtualNetworks []*armnetwork.VirtualNetwork) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, virtualNetwork := range virtualNetworks {
		name := *virtualNetwork.Name
		tferName := terraformutils.TfSanitize(name)
		for _, resource := range resources {
			if tferName == resource.ResourceName {
				name = name + "_" + *virtualNetwork.ID
			}
		}

		resources = append(resources, terraformutils.NewSimpleResource(
			*virtualNetwork.ID,
			name,
			"azurerm_virtual_network",
			g.ProviderName))
	}
	return resources
}

func (g *VirtualNetworkGenerator) InitResources() error {
	ctx := context.Background()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	virtualNetworkClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}

	var virtualNetworks []*armnetwork.VirtualNetwork
	if resourceGroup != "" {
		virtualNetworks, err = listAll(ctx, virtualNetworkClient.NewListPager(resourceGroup, nil),
			func(p armnetwork.VirtualNetworksClientListResponse) []*armnetwork.VirtualNetwork { return p.Value })
	} else {
		virtualNetworks, err = listAll(ctx, virtualNetworkClient.NewListAllPager(nil),
			func(p armnetwork.VirtualNetworksClientListAllResponse) []*armnetwork.VirtualNetwork { return p.Value })
	}
	g.Resources = g.createResources(virtualNetworks)
	return err
}
