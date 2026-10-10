// Copyright 2020 The Terraformer Authors.
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
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerinstance/armcontainerinstance/v2"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry/v3"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type ContainerGenerator struct {
	AzureService
}

func (g *ContainerGenerator) listAndAddForContainerGroup() ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := g.Context()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	containerGroupsClient, err := armcontainerinstance.NewContainerGroupsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	var containerGroups []*armcontainerinstance.ContainerGroup
	if resourceGroup != "" {
		containerGroups, err = listAll(ctx, containerGroupsClient.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armcontainerinstance.ContainerGroupsClientListByResourceGroupResponse) []*armcontainerinstance.ContainerGroup {
				return p.Value
			})
	} else {
		containerGroups, err = listAll(ctx, containerGroupsClient.NewListPager(nil),
			func(p armcontainerinstance.ContainerGroupsClientListResponse) []*armcontainerinstance.ContainerGroup {
				return p.Value
			})
	}
	for _, containerGroup := range containerGroups {
		resources = append(resources, terraformutils.NewSimpleResource(
			*containerGroup.ID,
			*containerGroup.Name,
			"azurerm_container_group",
			g.ProviderName))
	}

	return resources, err
}

func (g *ContainerGenerator) listRegistryWebhooks(resourceGroupName string, registryName string) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := g.Context()
	subscriptionID, _, credential, options := g.getClientArgs()
	webhooksClient, err := armcontainerregistry.NewWebhooksClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	webhooks, err := listAllLenient(ctx, webhooksClient.NewListPager(resourceGroupName, registryName, nil),
		func(p armcontainerregistry.WebhooksClientListResponse) []*armcontainerregistry.Webhook {
			return p.Value
		})
	if err != nil {
		return nil, err
	}
	for _, webhook := range webhooks {
		resources = append(resources, terraformutils.NewSimpleResource(
			*webhook.ID,
			*webhook.Name,
			"azurerm_container_registry_webhook",
			g.ProviderName))
	}
	return resources, nil
}

func (g *ContainerGenerator) listAndAddForContainerRegistry() ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := g.Context()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	containerRegistriesClient, err := armcontainerregistry.NewRegistriesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	var containerRegistries []*armcontainerregistry.Registry
	if resourceGroup != "" {
		containerRegistries, err = listAll(ctx, containerRegistriesClient.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armcontainerregistry.RegistriesClientListByResourceGroupResponse) []*armcontainerregistry.Registry {
				return p.Value
			})
	} else {
		containerRegistries, err = listAll(ctx, containerRegistriesClient.NewListPager(nil),
			func(p armcontainerregistry.RegistriesClientListResponse) []*armcontainerregistry.Registry {
				return p.Value
			})
	}
	if err != nil {
		return nil, err
	}
	for _, containerRegistry := range containerRegistries {
		resources = append(resources, terraformutils.NewSimpleResource(
			*containerRegistry.ID,
			*containerRegistry.Name,
			"azurerm_container_registry",
			g.ProviderName))

		id, err := ParseAzureResourceID(*containerRegistry.ID)
		if err != nil {
			return nil, err
		}

		webhooks, err := g.listRegistryWebhooks(id.ResourceGroup, *containerRegistry.Name)
		if err != nil {
			return nil, err
		}
		resources = append(resources, webhooks...)
	}

	return resources, nil
}

func (g *ContainerGenerator) InitResources() error {
	functions := []func() ([]terraformutils.Resource, error){
		g.listAndAddForContainerGroup,
		g.listAndAddForContainerRegistry,
	}

	for _, f := range functions {
		resources, err := f()
		if err != nil {
			return err
		}
		g.Resources = append(g.Resources, resources...)
	}

	return nil
}
