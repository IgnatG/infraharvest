package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v6"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type AppServiceGenerator struct {
	AzureService
}

func (g AppServiceGenerator) listApps() ([]terraformutils.Resource, error) {
	var resources []terraformutils.Resource
	ctx := context.Background()
	subscriptionID, resourceGroup, credential, options := g.getClientArgs()
	appServiceClient, err := armappservice.NewWebAppsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}

	var sites []*armappservice.Site
	if resourceGroup != "" {
		sites, err = listAll(ctx, appServiceClient.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armappservice.WebAppsClientListByResourceGroupResponse) []*armappservice.Site { return p.Value })
	} else {
		sites, err = listAll(ctx, appServiceClient.NewListPager(nil),
			func(p armappservice.WebAppsClientListResponse) []*armappservice.Site { return p.Value })
	}
	for _, site := range sites {
		resources = append(resources, terraformutils.NewSimpleResource(
			*site.ID,
			*site.Name,
			"azurerm_app_service",
			g.ProviderName))
	}
	if err != nil {
		return resources, err
	}

	return resources, nil
}

func (g *AppServiceGenerator) InitResources() error {
	resources, err := g.listApps()
	if err != nil {
		return err
	}

	g.Resources = append(g.Resources, resources...)

	return nil
}
