// Copyright 2021 The Terraformer Authors.
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
	"fmt"
	"log"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/datafactory/armdatafactory/v11"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type DataFactoryGenerator struct {
	AzureService
}

// Maps item.Properties.Type -> terraform.ResoruceType
// Information extracted from
//   SupportedResources are in:
//   @ github.com/azure/azure-sdk-for-go@v42.3.0+incompatible/services/datafactory/mgmt/2018-06-01/datafactory/models.go
//   PossibleTypeBasicDatasetValues, PossibleTypeBasicIntegrationRuntimeValues, PossibleTypeBasicLinkedServiceValues, PossibleTypeBasicTriggerValues
//   TypeBasicDataset,TypeBasicIntegrationRuntime, TypeBasicLinkedService, TypeBasicTrigger, TypeBasicDataFlow

var (
	SupportedResources = map[string]string{
		"AzureBlob":                "azurerm_data_factory_dataset_azure_blob",
		"Binary":                   "azurerm_data_factory_dataset_binary",
		"CosmosDbSqlApiCollection": "azurerm_data_factory_dataset_cosmosdb_sqlapi",
		"CustomDataset":            "azurerm_data_factory_custom_dataset",
		"DelimitedText":            "azurerm_data_factory_dataset_delimited_text",
		"HttpFile":                 "azurerm_data_factory_dataset_http",
		"Json":                     "azurerm_data_factory_dataset_json",
		"MySqlTable":               "azurerm_data_factory_dataset_mysql",
		"Parquet":                  "azurerm_data_factory_dataset_parquet",
		"PostgreSqlTable":          "azurerm_data_factory_dataset_postgresql",
		"SnowflakeTable":           "azurerm_data_factory_dataset_snowflake",
		"SqlServerTable":           "azurerm_data_factory_dataset_sql_server_table",
		"IntegrationRuntime":       "azurerm_data_factory_integration_runtime_azure",
		"Managed":                  "azurerm_data_factory_integration_runtime_azure_ssis",
		"SelfHosted":               "azurerm_data_factory_integration_runtime_self_hosted",
		"AzureBlobStorage":         "azurerm_data_factory_linked_service_azure_blob_storage",
		"AzureDatabricks":          "azurerm_data_factory_linked_service_azure_databricks",
		"AzureFileStorage":         "azurerm_data_factory_linked_service_azure_file_storage",
		"AzureFunction":            "azurerm_data_factory_linked_service_azure_function",
		"AzureSearch":              "azurerm_data_factory_linked_service_azure_search",
		"AzureSqlDatabase":         "azurerm_data_factory_linked_service_azure_sql_database",
		"AzureTableStorage":        "azurerm_data_factory_linked_service_azure_table_storage",
		"CosmosDb":                 "azurerm_data_factory_linked_service_cosmosdb",
		"CustomDataSource":         "azurerm_data_factory_linked_custom_service",
		"AzureBlobFS":              "azurerm_data_factory_linked_service_data_lake_storage_gen2",
		"AzureKeyVault":            "azurerm_data_factory_linked_service_key_vault",
		"AzureDataExplore":         "azurerm_data_factory_linked_service_kusto",
		"MySql":                    "azurerm_data_factory_linked_service_mysql",
		"OData":                    "azurerm_data_factory_linked_service_odata",
		"PostgreSql":               "azurerm_data_factory_linked_service_postgresql",
		"Sftp":                     "azurerm_data_factory_linked_service_sftp",
		"Snowflake":                "azurerm_data_factory_linked_service_snowflake",
		"SqlServer":                "azurerm_data_factory_linked_service_sql_server",
		"AzureSqlDW":               "azurerm_data_factory_linked_service_synapse",
		"Web":                      "azurerm_data_factory_linked_service_web",
		"BlobEventsTrigger":        "azurerm_data_factory_trigger_blob_event",
		"ScheduleTrigger":          "azurerm_data_factory_trigger_schedule",
		"TumblingWindowTrigger":    "azurerm_data_factory_trigger_tumbling_window",
	}
)

func getResourceTypeFrom(azureResourceName string) string {
	return SupportedResources[azureResourceName]
}

// linkedServiceType returns the type of a linked service, such as
// "AzureBlobStorage", or "" when there is none.
func linkedServiceType(properties armdatafactory.LinkedServiceClassification) string {
	if properties == nil {
		return ""
	}
	if base := properties.GetLinkedService(); base != nil && base.Type != nil {
		return *base.Type
	}
	return ""
}

// triggerType returns the type of a trigger, such as "ScheduleTrigger", or ""
// when there is none.
func triggerType(properties armdatafactory.TriggerClassification) string {
	if properties == nil {
		return ""
	}
	if base := properties.GetTrigger(); base != nil && base.Type != nil {
		return *base.Type
	}
	return ""
}

// datasetType returns the type of a dataset, such as "DelimitedText", or ""
// when there is none.
func datasetType(properties armdatafactory.DatasetClassification) string {
	if properties == nil {
		return ""
	}
	if base := properties.GetDataset(); base != nil && base.Type != nil {
		return *base.Type
	}
	return ""
}

func (az *AzureService) appendResourceAs(resources []terraformutils.Resource, itemID string, itemName string, resourceType string, abbreviation string) []terraformutils.Resource {
	prefix := strings.ReplaceAll(resourceType, resourceType, abbreviation)
	suffix := strings.ReplaceAll(itemName, "-", "_")
	resourceName := prefix + "_" + suffix
	res := terraformutils.NewSimpleResource(itemID, resourceName, resourceType, az.ProviderName)
	resources = append(resources, res)
	return resources
}

func (az *DataFactoryGenerator) appendResourceFrom(resources []terraformutils.Resource, id string, name string, azureType string) []terraformutils.Resource {
	if azureType != "" {
		resourceType := getResourceTypeFrom(azureType)
		if resourceType == "" {
			msg := fmt.Sprintf(`azurerm_data_factory: resource "%s" id: %s type: %s not handled yet by terraform or infraharvest`, name, id, azureType)
			log.Println(msg)
		} else {
			resources = az.appendResourceAs(resources, id, name, resourceType, "adf")
		}
	}
	return resources
}

func (az *DataFactoryGenerator) listFactories() ([]*armdatafactory.Factory, error) {
	subscriptionID, resourceGroup, credential, options := az.getClientArgs()
	client, err := armdatafactory.NewFactoriesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if resourceGroup != "" {
		return listAll(ctx, client.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armdatafactory.FactoriesClientListByResourceGroupResponse) []*armdatafactory.Factory {
				return p.Value
			})
	}
	return listAll(ctx, client.NewListPager(nil),
		func(p armdatafactory.FactoriesClientListResponse) []*armdatafactory.Factory { return p.Value })
}

func (az *DataFactoryGenerator) createDataFactoryResources(dataFactories []*armdatafactory.Factory) ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	for _, item := range dataFactories {
		resources = az.appendResourceAs(resources, *item.ID, *item.Name, "azurerm_data_factory", "adf")
	}
	return resources, nil
}

// getIntegrationRuntimeType tells the kinds of integration runtime apart: a
// self-hosted one, an Azure one (managed, without SSIS) and an Azure-SSIS one.
func getIntegrationRuntimeType(properties armdatafactory.IntegrationRuntimeClassification) string {
	if properties != nil {
		if base := properties.GetIntegrationRuntime(); base != nil && base.Type != nil &&
			*base.Type == armdatafactory.IntegrationRuntimeTypeSelfHosted {
			return "azurerm_data_factory_integration_runtime_self_hosted"
		}
	}
	if managed, ok := properties.(*armdatafactory.ManagedIntegrationRuntime); ok {
		if managed.TypeProperties == nil || managed.TypeProperties.SsisProperties == nil {
			return "azurerm_data_factory_integration_runtime_azure"
		}
	}
	return "azurerm_data_factory_integration_runtime_azure_ssis"
}

// factoryResourceGroup returns the resource group of a data factory.
func factoryResourceGroup(factory *armdatafactory.Factory) (string, error) {
	id, err := ParseAzureResourceID(*factory.ID)
	if err != nil {
		return "", err
	}
	return id.ResourceGroup, nil
}

func (az *DataFactoryGenerator) createIntegrationRuntimesResources(dataFactories []*armdatafactory.Factory) ([]terraformutils.Resource, error) {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armdatafactory.NewIntegrationRuntimesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	var resources []terraformutils.Resource
	for _, factory := range dataFactories {
		resourceGroup, err := factoryResourceGroup(factory)
		if err != nil {
			return nil, err
		}
		items, err := listAll(ctx, client.NewListByFactoryPager(resourceGroup, *factory.Name, nil),
			func(p armdatafactory.IntegrationRuntimesClientListByFactoryResponse) []*armdatafactory.IntegrationRuntimeResource {
				return p.Value
			})
		for _, item := range items {
			resourceType := getIntegrationRuntimeType(item.Properties)
			resources = az.appendResourceAs(resources, *item.ID, *item.Name, resourceType, "adfr")
		}
		if err != nil {
			return resources, err
		}
	}
	return resources, nil
}

func (az *DataFactoryGenerator) createLinkedServiceResources(dataFactories []*armdatafactory.Factory) ([]terraformutils.Resource, error) {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armdatafactory.NewLinkedServicesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	var resources []terraformutils.Resource
	for _, factory := range dataFactories {
		resourceGroup, err := factoryResourceGroup(factory)
		if err != nil {
			return nil, err
		}
		items, err := listAll(ctx, client.NewListByFactoryPager(resourceGroup, *factory.Name, nil),
			func(p armdatafactory.LinkedServicesClientListByFactoryResponse) []*armdatafactory.LinkedServiceResource {
				return p.Value
			})
		for _, item := range items {
			resources = az.appendResourceFrom(resources, *item.ID, *item.Name, linkedServiceType(item.Properties))
		}
		if err != nil {
			return resources, err
		}
	}
	return resources, nil
}

func (az *DataFactoryGenerator) createPipelineResources(dataFactories []*armdatafactory.Factory) ([]terraformutils.Resource, error) {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armdatafactory.NewPipelinesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	var resources []terraformutils.Resource
	for _, factory := range dataFactories {
		resourceGroup, err := factoryResourceGroup(factory)
		if err != nil {
			return nil, err
		}
		items, err := listAll(ctx, client.NewListByFactoryPager(resourceGroup, *factory.Name, nil),
			func(p armdatafactory.PipelinesClientListByFactoryResponse) []*armdatafactory.PipelineResource {
				return p.Value
			})
		for _, item := range items {
			resources = az.appendResourceAs(resources, *item.ID, *item.Name, "azurerm_data_factory_pipeline", "adfp")
		}
		if err != nil {
			return resources, err
		}
	}
	return resources, nil
}

func (az *DataFactoryGenerator) createPipelineTriggerScheduleResources(dataFactories []*armdatafactory.Factory) ([]terraformutils.Resource, error) {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armdatafactory.NewTriggersClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	var resources []terraformutils.Resource
	for _, factory := range dataFactories {
		resourceGroup, err := factoryResourceGroup(factory)
		if err != nil {
			return nil, err
		}
		items, err := listAll(ctx, client.NewListByFactoryPager(resourceGroup, *factory.Name, nil),
			func(p armdatafactory.TriggersClientListByFactoryResponse) []*armdatafactory.TriggerResource {
				return p.Value
			})
		for _, item := range items {
			resources = az.appendResourceFrom(resources, *item.ID, *item.Name, triggerType(item.Properties))
		}
		if err != nil {
			return resources, err
		}
	}
	return resources, nil
}

func (az *DataFactoryGenerator) createDataFlowResources(dataFactories []*armdatafactory.Factory) ([]terraformutils.Resource, error) {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armdatafactory.NewDataFlowsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	var resources []terraformutils.Resource
	for _, factory := range dataFactories {
		resourceGroup, err := factoryResourceGroup(factory)
		if err != nil {
			return nil, err
		}
		items, err := listAll(ctx, client.NewListByFactoryPager(resourceGroup, *factory.Name, nil),
			func(p armdatafactory.DataFlowsClientListByFactoryResponse) []*armdatafactory.DataFlowResource {
				return p.Value
			})
		for _, item := range items {
			resources = az.appendResourceAs(resources, *item.ID, *item.Name, "azurerm_data_factory_data_flow", "adfl")
		}
		if err != nil {
			return resources, err
		}
	}
	return resources, nil
}

func (az *DataFactoryGenerator) createPipelineDatasetResources(dataFactories []*armdatafactory.Factory) ([]terraformutils.Resource, error) {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armdatafactory.NewDatasetsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	var resources []terraformutils.Resource
	for _, factory := range dataFactories {
		resourceGroup, err := factoryResourceGroup(factory)
		if err != nil {
			return nil, err
		}
		items, err := listAll(ctx, client.NewListByFactoryPager(resourceGroup, *factory.Name, nil),
			func(p armdatafactory.DatasetsClientListByFactoryResponse) []*armdatafactory.DatasetResource {
				return p.Value
			})
		for _, item := range items {
			resources = az.appendResourceFrom(resources, *item.ID, *item.Name, datasetType(item.Properties))
		}
		if err != nil {
			return resources, err
		}
	}
	return resources, nil
}

func (az *DataFactoryGenerator) InitResources() error {

	dataFactories, err := az.listFactories()
	if err != nil {
		return err
	}

	factoriesFunctions := []func([]*armdatafactory.Factory) ([]terraformutils.Resource, error){
		az.createDataFactoryResources,
		az.createIntegrationRuntimesResources,
		az.createLinkedServiceResources,
		az.createPipelineResources,
		az.createPipelineTriggerScheduleResources,
		az.createPipelineDatasetResources,
		az.createDataFlowResources,
	}

	for _, f := range factoriesFunctions {
		resources, ero := f(dataFactories)
		if ero != nil {
			return ero
		}
		az.Resources = append(az.Resources, resources...)
	}
	return nil
}
