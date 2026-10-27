// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v11"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// SubnetLister lists the subnets of every virtual network the
// virtual_network service would list, the associations those subnets have
// with a route table, a network security group or a NAT gateway, and the
// service endpoint policies (azurerm_subnet_service_endpoint_storage_policy)
// in the same scope.
type SubnetLister struct {
	Service
}

// List lists the subnets, their associations and the service
// endpoint policies. Any listing that fails fails the service.
func (az *SubnetLister) List() error {
	subscriptionID, resourceGroup, credential, options := az.clientSettings()
	networks, err := az.listVirtualNetworks()
	if err != nil {
		return err
	}
	subnetsClient, err := armnetwork.NewSubnetsClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := az.Context()
	for _, network := range networks {
		if network.ID == nil || network.Name == nil {
			continue
		}
		subnets, err := az.subnetsOf(subnetsClient, network)
		if err != nil {
			return err
		}
		for _, subnet := range subnets {
			if subnet != nil {
				az.appendSubnet(subnet)
			}
		}
	}

	policiesClient, err := armnetwork.NewServiceEndpointPoliciesClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	var policies []*armnetwork.ServiceEndpointPolicy
	if resourceGroup != "" {
		policies, err = listAll(ctx, policiesClient.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armnetwork.ServiceEndpointPoliciesClientListByResourceGroupResponse) []*armnetwork.ServiceEndpointPolicy {
				return p.Value
			})
	} else {
		policies, err = listAll(ctx, policiesClient.NewListPager(nil),
			func(p armnetwork.ServiceEndpointPoliciesClientListResponse) []*armnetwork.ServiceEndpointPolicy {
				return p.Value
			})
	}
	if err != nil {
		return err
	}
	for _, policy := range policies {
		if policy.ID == nil || policy.Name == nil {
			continue
		}
		az.recordTagged(*policy.ID, *policy.Name, "azurerm_subnet_service_endpoint_storage_policy", policy.Tags)
	}
	return nil
}

// subnetsOf returns the subnets network carries in its properties, which a
// Resource Manager list returns in full, with the IDs of the route table,
// security group and NAT gateway each one is linked to. Only when network
// came back without the subnets field, as floci-az answers for subnets
// created on their own, are they listed with subnetsClient, under the
// network's own resource group, which is not --resource-group when the
// whole subscription is listed. floci-az answers an empty list rather than
// omitting the field, so an empty list is listed on its own too: a network
// with no subnets costs one request.
func (az *SubnetLister) subnetsOf(subnetsClient *armnetwork.SubnetsClient, network *armnetwork.VirtualNetwork) ([]*armnetwork.Subnet, error) {
	if network.Properties != nil && len(network.Properties.Subnets) > 0 {
		return network.Properties.Subnets, nil
	}
	networkResourceGroup, err := resourceGroupOf(*network.ID)
	if err != nil {
		return nil, err
	}
	return listAll(az.Context(), subnetsClient.NewListPager(networkResourceGroup, *network.Name, nil),
		func(p armnetwork.SubnetsClientListResponse) []*armnetwork.Subnet { return p.Value })
}

// appendSubnet adds subnet as azurerm_subnet, then one association for each
// of the route table, network security group and NAT gateway its properties
// reference. azurerm models each of those links as a resource of its own,
// imported by the subnet's ID. A subnet without an ID or a name is skipped.
func (az *SubnetLister) appendSubnet(subnet *armnetwork.Subnet) {
	if subnet.ID == nil || subnet.Name == nil {
		return
	}
	az.recordBare(*subnet.ID, *subnet.Name, "azurerm_subnet")
	properties := subnet.Properties
	if properties == nil {
		return
	}
	if properties.RouteTable != nil {
		az.appendAssociation(subnet, "route_table", properties.RouteTable.ID)
	}
	if properties.NetworkSecurityGroup != nil {
		az.appendAssociation(subnet, "network_security_group", properties.NetworkSecurityGroup.ID)
	}
	if properties.NatGateway != nil {
		az.appendAssociation(subnet, "nat_gateway", properties.NatGateway.ID)
	}
}

// appendAssociation adds an azurerm_subnet_<kind>_association between
// subnet and the resource whose ID is target, unless target is missing. Its
// ID is the subnet's, which is how azurerm imports these associations, and
// it records subnet_id and <kind>_id. Its label is
// "<subnet>_<kind>_association".
func (az *SubnetLister) appendAssociation(subnet *armnetwork.Subnet, kind string, target *string) {
	if target == nil || *target == "" {
		return
	}
	az.Resources = append(az.Resources, harvest.Record(
		*subnet.ID,
		*subnet.Name+"_"+kind+"_association",
		"azurerm_subnet_"+kind+"_association",
		az.Provider(),
		map[string]string{"subnet_id": *subnet.ID, kind + "_id": *target},
	))
}
