// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v11"
)

// PublicIPLister lists public IP addresses as azurerm_public_ip and
// public IP prefixes as azurerm_public_ip_prefix, in --resource-group when it
// is set, else in the whole subscription.
type PublicIPLister struct {
	Service
}

// List lists the addresses, then the prefixes. Either listing
// failing fails the service.
func (az *PublicIPLister) List() error {
	if err := az.appendAddresses(); err != nil {
		return err
	}
	return az.appendPrefixes()
}

// appendAddresses lists the public IP addresses.
func (az *PublicIPLister) appendAddresses() error {
	subscriptionID, resourceGroup, credential, options := az.clientSettings()
	client, err := armnetwork.NewPublicIPAddressesClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := az.Context()
	var addresses []*armnetwork.PublicIPAddress
	if resourceGroup != "" {
		addresses, err = listAll(ctx, client.NewListPager(resourceGroup, nil),
			func(p armnetwork.PublicIPAddressesClientListResponse) []*armnetwork.PublicIPAddress { return p.Value })
	} else {
		addresses, err = listAll(ctx, client.NewListAllPager(nil),
			func(p armnetwork.PublicIPAddressesClientListAllResponse) []*armnetwork.PublicIPAddress {
				return p.Value
			})
	}
	if err != nil {
		return err
	}
	for _, address := range addresses {
		if address.ID == nil || address.Name == nil {
			continue
		}
		az.recordTagged(*address.ID, *address.Name, "azurerm_public_ip", address.Tags)
	}
	return nil
}

// appendPrefixes lists the public IP prefixes.
func (az *PublicIPLister) appendPrefixes() error {
	subscriptionID, resourceGroup, credential, options := az.clientSettings()
	client, err := armnetwork.NewPublicIPPrefixesClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := az.Context()
	var prefixes []*armnetwork.PublicIPPrefix
	if resourceGroup != "" {
		prefixes, err = listAll(ctx, client.NewListPager(resourceGroup, nil),
			func(p armnetwork.PublicIPPrefixesClientListResponse) []*armnetwork.PublicIPPrefix { return p.Value })
	} else {
		prefixes, err = listAll(ctx, client.NewListAllPager(nil),
			func(p armnetwork.PublicIPPrefixesClientListAllResponse) []*armnetwork.PublicIPPrefix { return p.Value })
	}
	if err != nil {
		return err
	}
	for _, prefix := range prefixes {
		if prefix.ID == nil || prefix.Name == nil {
			continue
		}
		az.recordTagged(*prefix.ID, *prefix.Name, "azurerm_public_ip_prefix", prefix.Tags)
	}
	return nil
}
