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
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armlocks"
)

type ManagementLockGenerator struct {
	AzureService
}

func (az *ManagementLockGenerator) listResources() ([]*armlocks.ManagementLockObject, error) {
	subscriptionID, resourceGroup, credential, options := az.getClientArgs()
	client, err := armlocks.NewManagementLocksClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := az.Context()
	if resourceGroup != "" {
		return listAll(ctx, client.NewListAtResourceGroupLevelPager(resourceGroup, nil),
			func(p armlocks.ManagementLocksClientListAtResourceGroupLevelResponse) []*armlocks.ManagementLockObject {
				return p.Value
			})
	}
	return listAll(ctx, client.NewListAtSubscriptionLevelPager(nil),
		func(p armlocks.ManagementLocksClientListAtSubscriptionLevelResponse) []*armlocks.ManagementLockObject {
			return p.Value
		})
}

func (az *ManagementLockGenerator) appendResource(resource *armlocks.ManagementLockObject) {
	az.AppendSimpleResource(*resource.ID, *resource.Name, "azurerm_management_lock")
}

func (az *ManagementLockGenerator) InitResources() error {

	resources, err := az.listResources()
	if err != nil {
		return err
	}
	for _, resource := range resources {
		az.appendResource(resource)
	}
	return nil
}
