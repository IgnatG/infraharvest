// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v11"
)

// VirtualNetworkLister lists virtual networks as azurerm_virtual_network,
// in --resource-group when it is set, else in the whole subscription.
type VirtualNetworkLister struct {
	Service
}

// List lists the virtual networks.
func (az *VirtualNetworkLister) List() error {
	networks, err := az.listVirtualNetworks()
	if err != nil {
		return err
	}
	for _, network := range networks {
		if network.ID == nil || network.Name == nil {
			continue
		}
		az.recordTagged(*network.ID, *network.Name, "azurerm_virtual_network", network.Tags)
	}
	return nil
}

// listVirtualNetworks returns the virtual networks in --resource-group, or
// in the subscription when no group is set. It lives on Service because
// the subnet lister walks the same networks.
func (az *Service) listVirtualNetworks() ([]*armnetwork.VirtualNetwork, error) {
	subscriptionID, resourceGroup, credential, options := az.clientSettings()
	client, err := armnetwork.NewVirtualNetworksClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := az.Context()
	if resourceGroup != "" {
		return listAll(ctx, client.NewListPager(resourceGroup, nil),
			func(p armnetwork.VirtualNetworksClientListResponse) []*armnetwork.VirtualNetwork { return p.Value })
	}
	return listAll(ctx, client.NewListAllPager(nil),
		func(p armnetwork.VirtualNetworksClientListAllResponse) []*armnetwork.VirtualNetwork { return p.Value })
}

// resourceGroupOf returns the resource group an ARM resource ID names. Child
// listings (a network's subnets, a security group's rules) need the parent's
// own group, which is not --resource-group when the whole subscription is
// listed.
func resourceGroupOf(id string) (string, error) {
	parsed, err := arm.ParseResourceID(id)
	if err != nil {
		return "", err
	}
	return parsed.ResourceGroupName, nil
}
