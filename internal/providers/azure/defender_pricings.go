// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"strconv"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/security/armsecurity"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// SecurityCenterSubscriptionPricingLister lists the subscription's
// Microsoft Defender for Cloud plans, one pricing per plan (such as
// VirtualMachines or StorageAccounts), as
// azurerm_security_center_subscription_pricing, each with its tier (Free
// or Standard), its subplan and whether the plan is deprecated, as the
// list returns them, so the catalogue can leave out the plans left on
// Free. Pricings belong to the subscription, not to a resource group, so
// with --resource-group there is nothing to list. Resource Graph keeps
// them apart from the resources it indexes, so resource_graph doesn't list
// them.
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
		az.Resources = append(az.Resources, harvest.Record(*pricing.ID, *pricing.Name, "azurerm_security_center_subscription_pricing", az.Provider(), pricingAttributes(pricing.Properties)))
	}
	return nil
}

// pricingAttributes records what the list returned of a plan: its tier,
// subplan and deprecated, where set.
func pricingAttributes(p *armsecurity.PricingProperties) map[string]string {
	attributes := map[string]string{}
	if p == nil {
		return attributes
	}
	if p.PricingTier != nil {
		attributes["tier"] = string(*p.PricingTier)
	}
	if p.SubPlan != nil && *p.SubPlan != "" {
		attributes["subplan"] = *p.SubPlan
	}
	if p.Deprecated != nil {
		attributes["deprecated"] = strconv.FormatBool(*p.Deprecated)
	}
	return attributes
}
