package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/security/armsecurity"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type SecurityCenterSubscriptionPricingGenerator struct {
	AzureService
}

func (g SecurityCenterSubscriptionPricingGenerator) listSubscriptionPricing() ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()

	// Pricings are set per subscription, not per resource group.
	if resourceGroup != "" {
		return resources, nil
	}
	securityCenterPricingClient, err := armsecurity.NewPricingsClient(credential, options)
	if err != nil {
		return resources, err
	}
	pricingList, err := securityCenterPricingClient.List(ctx, "subscriptions/"+subscriptionID, nil)
	if err != nil {
		return resources, err
	}

	for _, pricing := range pricingList.Value {
		if pricing == nil {
			continue
		}
		resources = append(resources, terraformutils.NewSimpleResource(
			*pricing.ID,
			*pricing.Name,
			"azurerm_security_center_subscription_pricing",
			g.ProviderName))
	}

	return resources, nil
}

func (g *SecurityCenterSubscriptionPricingGenerator) InitResources() error {
	resources, err := g.listSubscriptionPricing()
	if err != nil {
		return err
	}

	g.Resources = append(g.Resources, resources...)

	return nil
}
