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
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault/v2"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type KeyVaultGenerator struct {
	AzureService
}

func (g KeyVaultGenerator) newResource(id, name *string) terraformutils.Resource {
	return terraformutils.NewSimpleResource(
		*id,
		*name,
		"azurerm_key_vault",
		"azurerm")
}

func (g KeyVaultGenerator) createResources(ctx context.Context, client *armkeyvault.VaultsClient) ([]terraformutils.Resource, error) {
	vaults, err := listAll(ctx, client.NewListPager(nil),
		func(p armkeyvault.VaultsClientListResponse) []*armkeyvault.TrackedResource { return p.Value })
	var resources []terraformutils.Resource
	for _, vault := range vaults {
		resources = append(resources, g.newResource(vault.ID, vault.Name))
	}
	return resources, err
}

func (g KeyVaultGenerator) createResourcesByResourceGroup(ctx context.Context, rg string, client *armkeyvault.VaultsClient) ([]terraformutils.Resource, error) {
	vaults, err := listAll(ctx, client.NewListByResourceGroupPager(rg, nil),
		func(p armkeyvault.VaultsClientListByResourceGroupResponse) []*armkeyvault.Vault { return p.Value })
	var resources []terraformutils.Resource
	for _, vault := range vaults {
		resources = append(resources, g.newResource(vault.ID, vault.Name))
	}
	return resources, err
}

func (g *KeyVaultGenerator) InitResources() error {
	ctx := g.Context()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	vaultsClient, err := armkeyvault.NewVaultsClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}

	if resourceGroup != "" {
		g.Resources, err = g.createResourcesByResourceGroup(ctx, resourceGroup, vaultsClient)
		return err
	}
	g.Resources, err = g.createResources(ctx, vaultsClient)
	return err
}
