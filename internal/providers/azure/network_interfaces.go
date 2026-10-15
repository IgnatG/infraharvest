// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v11"
)

// NetworkInterfaceLister lists network interfaces as
// azurerm_network_interface, in --resource-group when it is set, else in the
// whole subscription.
type NetworkInterfaceLister struct {
	Service
}

// List lists the network interfaces.
func (az *NetworkInterfaceLister) List() error {
	subscriptionID, resourceGroup, credential, options := az.clientSettings()
	client, err := armnetwork.NewInterfacesClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := az.Context()
	var interfaces []*armnetwork.Interface
	if resourceGroup != "" {
		interfaces, err = listAll(ctx, client.NewListPager(resourceGroup, nil),
			func(p armnetwork.InterfacesClientListResponse) []*armnetwork.Interface { return p.Value })
	} else {
		interfaces, err = listAll(ctx, client.NewListAllPager(nil),
			func(p armnetwork.InterfacesClientListAllResponse) []*armnetwork.Interface { return p.Value })
	}
	if err != nil {
		return err
	}
	for _, nic := range interfaces {
		if nic.ID == nil || nic.Name == nil {
			continue
		}
		az.recordBare(*nic.ID, *nic.Name, "azurerm_network_interface")
	}
	return nil
}
