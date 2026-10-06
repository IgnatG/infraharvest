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

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage/v4"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type StorageAccountGenerator struct {
	AzureService
}

func (g StorageAccountGenerator) createResources(accounts []*armstorage.Account) []terraformutils.Resource {
	var resources []terraformutils.Resource
	for _, account := range accounts {
		resources = append(resources, terraformutils.NewSimpleResource(
			*account.ID,
			*account.Name,
			"azurerm_storage_account",
			"azurerm"))
	}
	return resources
}

// listStorageAccounts lists the storage accounts of the resource group, or
// of the whole subscription when rg is "".
func listStorageAccounts(ctx context.Context, client *armstorage.AccountsClient, rg string) ([]*armstorage.Account, error) {
	if rg != "" {
		return listAll(ctx, client.NewListByResourceGroupPager(rg, nil),
			func(p armstorage.AccountsClientListByResourceGroupResponse) []*armstorage.Account { return p.Value })
	}
	return listAll(ctx, client.NewListPager(nil),
		func(p armstorage.AccountsClientListResponse) []*armstorage.Account { return p.Value })
}

func (g *StorageAccountGenerator) InitResources() error {
	ctx := context.Background()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	accountsClient, err := armstorage.NewAccountsClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	accounts, err := listStorageAccounts(ctx, accountsClient, resourceGroup)
	g.Resources = g.createResources(accounts)
	return err
}
