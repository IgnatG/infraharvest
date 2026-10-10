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

type NetworkInterfaceGenerator struct {
	AzureService
}

func (g NetworkInterfaceGenerator) createResources(interfaces []*armnetwork.Interface) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, networkInterface := range interfaces {
		resources = append(resources, terraformutils.NewSimpleResource(
			*networkInterface.ID,
			*networkInterface.Name,
			"azurerm_network_interface",
			"azurerm"))
	}
	return resources
}

func (g *NetworkInterfaceGenerator) InitResources() error {
	ctx := g.Context()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	interfacesClient, err := armnetwork.NewInterfacesClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}

	var interfaces []*armnetwork.Interface
	if resourceGroup != "" {
		interfaces, err = listAll(ctx, interfacesClient.NewListPager(resourceGroup, nil),
			func(p armnetwork.InterfacesClientListResponse) []*armnetwork.Interface { return p.Value })
	} else {
		interfaces, err = listAll(ctx, interfacesClient.NewListAllPager(nil),
			func(p armnetwork.InterfacesClientListAllResponse) []*armnetwork.Interface { return p.Value })
	}
	g.Resources = g.createResources(interfaces)
	return err
}
