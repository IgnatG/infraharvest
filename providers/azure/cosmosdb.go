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
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cosmos/armcosmos/v4"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type CosmosDBGenerator struct {
	AzureService
}

// cosmosDBSQLIDInOldFormat rewrites a Cosmos DB SQL database or container ID
// to the "databases" form the azurerm provider imports.
//
// NOTE:
// For a similar reason as
// https://github.com/terraform-providers/terraform-provider-azurerm/issues/7472#issuecomment-650684349
// The cosmosdb resource format change is NOT yet addressed in terraform provider
// This is a workaround to convert to old format, and might be removed if they deprecate the old format
func cosmosDBSQLIDInOldFormat(id string) string {
	return strings.Replace(id, "sqlDatabases", "databases", 1)
}

func (g *CosmosDBGenerator) listSQLDatabasesAndContainersBehind(resourceGroupName string, accountName string) ([]terraformutils.Resource, []terraformutils.Resource, error) {
	var resourcesDatabase []terraformutils.Resource
	var resourcesContainer []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	sqlResourcesClient, err := armcosmos.NewSQLResourcesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, nil, err
	}

	sqlDatabases, err := listAll(ctx, sqlResourcesClient.NewListSQLDatabasesPager(resourceGroupName, accountName, nil),
		func(p armcosmos.SQLResourcesClientListSQLDatabasesResponse) []*armcosmos.SQLDatabaseGetResults {
			return p.Value
		})
	if err != nil {
		return nil, nil, err
	}
	for _, sqlDatabase := range sqlDatabases {
		resourcesDatabase = append(resourcesDatabase, terraformutils.NewSimpleResource(
			cosmosDBSQLIDInOldFormat(*sqlDatabase.ID),
			*sqlDatabase.Name,
			"azurerm_cosmosdb_sql_database",
			g.ProviderName))

		sqlContainers, err := listAll(ctx,
			sqlResourcesClient.NewListSQLContainersPager(resourceGroupName, accountName, *sqlDatabase.Name, nil),
			func(p armcosmos.SQLResourcesClientListSQLContainersResponse) []*armcosmos.SQLContainerGetResults {
				return p.Value
			})
		if err != nil {
			return nil, nil, err
		}
		for _, sqlContainer := range sqlContainers {
			resourcesContainer = append(resourcesContainer, terraformutils.NewSimpleResource(
				cosmosDBSQLIDInOldFormat(*sqlContainer.ID),
				*sqlContainer.Name,
				"azurerm_cosmosdb_sql_container",
				g.ProviderName))
		}
	}

	return resourcesDatabase, resourcesContainer, nil
}

func (g *CosmosDBGenerator) listTables(resourceGroupName string, accountName string) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, _, credential, options := g.getClientArgs()
	tableResourcesClient, err := armcosmos.NewTableResourcesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	tables, err := listAll(ctx, tableResourcesClient.NewListTablesPager(resourceGroupName, accountName, nil),
		func(p armcosmos.TableResourcesClientListTablesResponse) []*armcosmos.TableGetResults { return p.Value })
	if err != nil {
		return nil, err
	}
	for _, table := range tables {
		resources = append(resources, terraformutils.NewSimpleResource(
			*table.ID,
			*table.Name,
			"azurerm_cosmosdb_table",
			g.ProviderName))
	}

	return resources, nil
}

func (g *CosmosDBGenerator) listAndAddForDatabaseAccounts() ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	databaseAccountsClient, err := armcosmos.NewDatabaseAccountsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	var accounts []*armcosmos.DatabaseAccountGetResults
	if resourceGroup != "" {
		accounts, err = listAll(ctx, databaseAccountsClient.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armcosmos.DatabaseAccountsClientListByResourceGroupResponse) []*armcosmos.DatabaseAccountGetResults {
				return p.Value
			})
	} else {
		accounts, err = listAll(ctx, databaseAccountsClient.NewListPager(nil),
			func(p armcosmos.DatabaseAccountsClientListResponse) []*armcosmos.DatabaseAccountGetResults {
				return p.Value
			})
	}
	if err != nil {
		return nil, err
	}
	for _, account := range accounts {
		resources = append(resources, terraformutils.NewSimpleResource(
			*account.ID,
			*account.Name,
			"azurerm_cosmosdb_account",
			g.ProviderName))

		id, err := ParseAzureResourceID(*account.ID)
		if err != nil {
			return nil, err
		}

		tables, err := g.listTables(id.ResourceGroup, *account.Name)
		if err != nil {
			return nil, err
		}
		resources = append(resources, tables...)

		sqlDatabases, sqlContainers, err := g.listSQLDatabasesAndContainersBehind(id.ResourceGroup, *account.Name)
		if err != nil {
			return nil, err
		}
		resources = append(resources, sqlDatabases...)
		resources = append(resources, sqlContainers...)
	}

	return resources, nil
}

func (g *CosmosDBGenerator) InitResources() error {
	functions := []func() ([]terraformutils.Resource, error){
		g.listAndAddForDatabaseAccounts,
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
