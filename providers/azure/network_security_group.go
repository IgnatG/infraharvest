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
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v11"
)

type NetworkSecurityGroupGenerator struct {
	AzureService
}

func (az *NetworkSecurityGroupGenerator) listResources() ([]*armnetwork.SecurityGroup, error) {
	subscriptionID, resourceGroup, credential, options := az.getClientArgs()
	client, err := armnetwork.NewSecurityGroupsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := az.Context()
	if resourceGroup != "" {
		return listAll(ctx, client.NewListPager(resourceGroup, nil),
			func(p armnetwork.SecurityGroupsClientListResponse) []*armnetwork.SecurityGroup { return p.Value })
	}
	return listAll(ctx, client.NewListAllPager(nil),
		func(p armnetwork.SecurityGroupsClientListAllResponse) []*armnetwork.SecurityGroup { return p.Value })
}

func (az *NetworkSecurityGroupGenerator) appendResource(resource *armnetwork.SecurityGroup) {
	az.AppendSimpleResourceWithDuplicateCheck(*resource.ID, *resource.Name, "azurerm_network_security_group")
}

func (az *NetworkSecurityGroupGenerator) appendRules(parent *armnetwork.SecurityGroup, resourceGroupID *ResourceID) error {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armnetwork.NewSecurityRulesClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := az.Context()
	rules, err := listAll(ctx, client.NewListPager(resourceGroupID.ResourceGroup, *parent.Name, nil),
		func(p armnetwork.SecurityRulesClientListResponse) []*armnetwork.SecurityRule { return p.Value })
	for _, item := range rules {
		az.AppendSimpleResourceWithDuplicateCheck(*item.ID, *item.Name, "azurerm_network_security_rule")
	}
	return err
}

func (az *NetworkSecurityGroupGenerator) InitResources() error {

	resources, err := az.listResources()
	if err != nil {
		return err
	}
	for _, resource := range resources {
		az.appendResource(resource)
		resourceGroupID, err := ParseAzureResourceID(*resource.ID)
		if err != nil {
			return err
		}
		err = az.appendRules(resource, resourceGroupID)
		if err != nil {
			return err
		}
	}
	return nil
}
