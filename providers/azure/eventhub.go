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
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventhub/armeventhub"
)

type EventHubGenerator struct {
	AzureService
}

func (az *EventHubGenerator) listNamespaces() ([]*armeventhub.EHNamespace, error) {
	subscriptionID, resourceGroup, credential, options := az.getClientArgs()
	client, err := armeventhub.NewNamespacesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := az.Context()
	if resourceGroup != "" {
		return listAll(ctx, client.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armeventhub.NamespacesClientListByResourceGroupResponse) []*armeventhub.EHNamespace {
				return p.Value
			})
	}
	return listAll(ctx, client.NewListPager(nil),
		func(p armeventhub.NamespacesClientListResponse) []*armeventhub.EHNamespace { return p.Value })
}

func (az *EventHubGenerator) AppendNamespace(namespace *armeventhub.EHNamespace) {
	az.AppendSimpleResource(*namespace.ID, *namespace.Name, "azurerm_eventhub_namespace")
}

func (az *EventHubGenerator) appendEventHubs(namespace *armeventhub.EHNamespace, namespaceRg *ResourceID) error {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armeventhub.NewEventHubsClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := az.Context()
	eventHubs, listErr := listAll(ctx, client.NewListByNamespacePager(namespaceRg.ResourceGroup, *namespace.Name, nil),
		func(p armeventhub.EventHubsClientListByNamespaceResponse) []*armeventhub.Eventhub { return p.Value })
	for _, item := range eventHubs {
		az.AppendSimpleResource(*item.ID, *item.Name, "azurerm_eventhub")
		err = az.appendConsumerGroups(namespace, namespaceRg, *item.Name)
		if err != nil {
			return err
		}
	}
	return listErr
}

func (az *EventHubGenerator) appendConsumerGroups(namespace *armeventhub.EHNamespace, namespaceRg *ResourceID, eventHubName string) error {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armeventhub.NewConsumerGroupsClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := az.Context()
	consumerGroups, err := listAll(ctx,
		client.NewListByEventHubPager(namespaceRg.ResourceGroup, *namespace.Name, eventHubName, nil),
		func(p armeventhub.ConsumerGroupsClientListByEventHubResponse) []*armeventhub.ConsumerGroup {
			return p.Value
		})
	for _, item := range consumerGroups {
		az.AppendSimpleResource(*item.ID, *item.Name, "azurerm_eventhub_consumer_group")
	}
	return err
}

func (az *EventHubGenerator) appendAuthorizationRules(namespace *armeventhub.EHNamespace, namespaceRg *ResourceID) error {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armeventhub.NewNamespacesClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := az.Context()
	rules, err := listAll(ctx, client.NewListAuthorizationRulesPager(namespaceRg.ResourceGroup, *namespace.Name, nil),
		func(p armeventhub.NamespacesClientListAuthorizationRulesResponse) []*armeventhub.AuthorizationRule {
			return p.Value
		})
	for _, item := range rules {
		az.AppendSimpleResource(*item.ID, *item.Name, "azurerm_eventhub_namespace_authorization_rule")
	}
	return err
}

func (az *EventHubGenerator) InitResources() error {

	namespaces, err := az.listNamespaces()
	if err != nil {
		return err
	}
	for _, namespace := range namespaces {
		az.AppendNamespace(namespace)
		namespaceRg, err := ParseAzureResourceID(*namespace.ID)
		if err != nil {
			return err
		}
		err = az.appendEventHubs(namespace, namespaceRg)
		if err != nil {
			return err
		}
		err = az.appendAuthorizationRules(namespace, namespaceRg)
		if err != nil {
			return err
		}
	}
	return nil
}
