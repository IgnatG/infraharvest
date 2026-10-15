// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/security/armsecurity"
)

// SecurityCenterSubscriptionPricingLister lists the subscription's
// Microsoft Defender for Cloud plans, one pricing per plan (such as
// VirtualMachines or StorageAccounts), as
// azurerm_security_center_subscription_pricing. Pricings belong to the
// subscription, not to a resource group, so with --resource-group there is
// nothing to list. Resource Graph keeps them apart from the resources it
// indexes, so resource_graph doesn't list them.
type SecurityCenterSubscriptionPricingLister struct {
	Service
}

// List lists the pricings, unless --resource-group is set. The
// list isn't paged: one call returns every plan.
func (az *SecurityCenterSubscriptionPricingLister) List() error {
	subscriptionID, resourceGroup, credential, options := az.clientSettings()
	if resourceGroup != "" {
		return nil
	}
	client, err := armsecurity.NewPricingsClient(credential, options)
	if err != nil {
		return err
	}
	pricings, err := client.List(az.Context(), "subscriptions/"+subscriptionID, nil)
	if err != nil {
		return err
	}
	for _, pricing := range pricings.Value {
		if pricing == nil || pricing.ID == nil || pricing.Name == nil {
			continue
		}
		az.recordBare(*pricing.ID, *pricing.Name, "azurerm_security_center_subscription_pricing")
	}
	return nil
}
