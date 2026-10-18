// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"maps"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v11"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// NetworkInterfaceLister lists network interfaces as
// azurerm_network_interface, in --resource-group when it is set, else in the
// whole subscription. An interface a private endpoint or a Private Link
// service created is recorded with it, as resource_graph records it
// (private_endpoint, private_link_service), so the catalogue leaves it out.
// Every interface is recorded with its tags, tags.<key>.
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
		attributes := interfaceOwners(nic)
		maps.Copy(attributes, armTagAttributes(nic.Tags))
		if len(attributes) == 0 {
			az.recordBare(*nic.ID, *nic.Name, "azurerm_network_interface")
			continue
		}
		az.Resources = append(az.Resources, harvest.Record(*nic.ID, *nic.Name, "azurerm_network_interface", az.Provider(), attributes))
	}
	return nil
}

// interfaceOwners returns the private endpoint and the Private Link
// service that created nic, if one did, as private_endpoint and
// private_link_service.
func interfaceOwners(nic *armnetwork.Interface) map[string]string {
	attributes := map[string]string{}
	if p := nic.Properties; p != nil {
		if p.PrivateEndpoint != nil && p.PrivateEndpoint.ID != nil && *p.PrivateEndpoint.ID != "" {
			attributes["private_endpoint"] = *p.PrivateEndpoint.ID
		}
		if p.PrivateLinkService != nil && p.PrivateLinkService.ID != nil && *p.PrivateLinkService.ID != "" {
			attributes["private_link_service"] = *p.PrivateLinkService.ID
		}
	}
	return attributes
}
