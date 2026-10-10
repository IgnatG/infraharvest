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
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources/v4"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type ResourceGroupGenerator struct {
	AzureService
}

func (g ResourceGroupGenerator) createResources(groups []*armresources.ResourceGroup) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, group := range groups {
		resources = append(resources, terraformutils.NewSimpleResource(
			*group.ID,
			*group.Name,
			"azurerm_resource_group",
			"azurerm"))
	}
	return resources
}

func (g *ResourceGroupGenerator) InitResources() error {
	ctx := g.Context()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	groupsClient, err := armresources.NewResourceGroupsClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}

	if resourceGroup != "" {
		group, err := groupsClient.Get(ctx, resourceGroup, nil)
		if err != nil {
			return err
		}
		g.Resources = []terraformutils.Resource{
			terraformutils.NewSimpleResource(
				*group.ID,
				*group.Name,
				"azurerm_resource_group",
				"azurerm"),
		}
		return nil
	}
	groups, err := listAllLenient(ctx, groupsClient.NewListPager(nil),
		func(p armresources.ResourceGroupsClientListResponse) []*armresources.ResourceGroup { return p.Value })
	if err != nil {
		return err
	}
	g.Resources = g.createResources(groups)
	return nil
}
