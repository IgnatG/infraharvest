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

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v11"
)

type SubnetGenerator struct {
	AzureService
}

func (az *SubnetGenerator) lisSubnets() ([]*armnetwork.Subnet, error) {
	subscriptionID, resourceGroup, credential, options := az.getClientArgs()
	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	var vnets []*armnetwork.VirtualNetwork
	if resourceGroup != "" {
		vnets, err = listAll(ctx, vnetClient.NewListPager(resourceGroup, nil),
			func(p armnetwork.VirtualNetworksClientListResponse) []*armnetwork.VirtualNetwork { return p.Value })
	} else {
		vnets, err = listAll(ctx, vnetClient.NewListAllPager(nil),
			func(p armnetwork.VirtualNetworksClientListAllResponse) []*armnetwork.VirtualNetwork { return p.Value })
	}
	if err != nil {
		return nil, err
	}
	var resources []*armnetwork.Subnet
	for _, vnet := range vnets {
		vnetID, err := ParseAzureResourceID(*vnet.ID)
		if err != nil {
			return nil, err
		}
		subnets, err := listAll(ctx, subnetClient.NewListPager(vnetID.ResourceGroup, *vnet.Name, nil),
			func(p armnetwork.SubnetsClientListResponse) []*armnetwork.Subnet { return p.Value })
		resources = append(resources, subnets...)
		if err != nil {
			return resources, err
		}
	}
	return resources, nil
}

func (az *SubnetGenerator) AppendSubnet(subnet *armnetwork.Subnet) {
	az.AppendSimpleResource(*subnet.ID, *subnet.Name, "azurerm_subnet")
}

func (az *SubnetGenerator) appendRouteTable(subnet *armnetwork.Subnet) {
	if props := subnet.Properties; props != nil {
		if prop := props.RouteTable; prop != nil {
			az.appendSimpleAssociation(
				*subnet.ID, *subnet.Name, prop.Name,
				"azurerm_subnet_route_table_association",
				map[string]string{
					"subnet_id":      *subnet.ID,
					"route_table_id": *prop.ID,
				})
		}
	}
}

func (az *SubnetGenerator) appendNetworkSecurityGroupAssociation(subnet *armnetwork.Subnet) {
	if props := subnet.Properties; props != nil {
		if prop := props.NetworkSecurityGroup; prop != nil {
			az.appendSimpleAssociation(
				*subnet.ID, *subnet.Name, prop.Name,
				"azurerm_subnet_network_security_group_association",
				map[string]string{
					"subnet_id":                 *subnet.ID,
					"network_security_group_id": *prop.ID,
				})
		}
	}
}

func (az *SubnetGenerator) appendNatGateway(subnet *armnetwork.Subnet) {
	if props := subnet.Properties; props != nil {
		if prop := props.NatGateway; prop != nil {
			az.appendSimpleAssociation(
				*subnet.ID, *subnet.Name, nil,
				"azurerm_subnet_nat_gateway_association",
				map[string]string{
					"subnet_id":      *subnet.ID,
					"nat_gateway_id": *prop.ID,
				})
		}
	}
}

func (az *SubnetGenerator) appendServiceEndpointPolicies() error {
	subscriptionID, resourceGroup, credential, options := az.getClientArgs()
	client, err := armnetwork.NewServiceEndpointPoliciesClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := context.Background()
	var policies []*armnetwork.ServiceEndpointPolicy
	if resourceGroup != "" {
		policies, err = listAll(ctx, client.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armnetwork.ServiceEndpointPoliciesClientListByResourceGroupResponse) []*armnetwork.ServiceEndpointPolicy {
				return p.Value
			})
	} else {
		policies, err = listAll(ctx, client.NewListPager(nil),
			func(p armnetwork.ServiceEndpointPoliciesClientListResponse) []*armnetwork.ServiceEndpointPolicy {
				return p.Value
			})
	}

	for _, item := range policies {
		az.AppendSimpleResource(*item.ID, *item.Name, "azurerm_subnet_service_endpoint_storage_policy")
	}
	return err
}

func (az *SubnetGenerator) InitResources() error {

	subnets, err := az.lisSubnets()
	if err != nil {
		return err
	}
	for _, subnet := range subnets {
		az.AppendSubnet(subnet)
		az.appendRouteTable(subnet)
		az.appendNetworkSecurityGroupAssociation(subnet)
		az.appendNatGateway(subnet)
	}
	if err := az.appendServiceEndpointPolicies(); err != nil {
		return err
	}
	return nil
}
