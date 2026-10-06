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

type RouteTableGenerator struct {
	AzureService
}

func (az *RouteTableGenerator) listResources() ([]*armnetwork.RouteTable, error) {
	subscriptionID, resourceGroup, credential, options := az.getClientArgs()
	client, err := armnetwork.NewRouteTablesClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if resourceGroup != "" {
		return listAll(ctx, client.NewListPager(resourceGroup, nil),
			func(p armnetwork.RouteTablesClientListResponse) []*armnetwork.RouteTable { return p.Value })
	}
	return listAll(ctx, client.NewListAllPager(nil),
		func(p armnetwork.RouteTablesClientListAllResponse) []*armnetwork.RouteTable { return p.Value })
}

func (az *RouteTableGenerator) appendResource(resource *armnetwork.RouteTable) {
	az.AppendSimpleResourceWithDuplicateCheck(*resource.ID, *resource.Name, "azurerm_route_table")
}

func (az *RouteTableGenerator) appendRoutes(parent *armnetwork.RouteTable, resourceGroupID *ResourceID) error {
	subscriptionID, _, credential, options := az.getClientArgs()
	client, err := armnetwork.NewRoutesClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := context.Background()
	routes, err := listAll(ctx, client.NewListPager(resourceGroupID.ResourceGroup, *parent.Name, nil),
		func(p armnetwork.RoutesClientListResponse) []*armnetwork.Route { return p.Value })
	for _, item := range routes {
		az.AppendSimpleResourceWithDuplicateCheck(*item.ID, *item.Name, "azurerm_route")
	}
	return err
}

func (az *RouteTableGenerator) listRouteFilters() ([]*armnetwork.RouteFilter, error) {
	subscriptionID, resourceGroup, credential, options := az.getClientArgs()
	client, err := armnetwork.NewRouteFiltersClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if resourceGroup != "" {
		return listAll(ctx, client.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armnetwork.RouteFiltersClientListByResourceGroupResponse) []*armnetwork.RouteFilter {
				return p.Value
			})
	}
	return listAll(ctx, client.NewListPager(nil),
		func(p armnetwork.RouteFiltersClientListResponse) []*armnetwork.RouteFilter { return p.Value })
}

func (az *RouteTableGenerator) appendRouteFilters(resource *armnetwork.RouteFilter) {
	az.AppendSimpleResource(*resource.ID, *resource.Name, "azurerm_route_filter")
}

func (az *RouteTableGenerator) InitResources() error {

	resources, err := az.listResources()
	if err != nil {
		return err
	}
	for _, resource := range resources {
		az.appendResource(resource)
		resourceGroupID, err := ParseAzureResourceID(*resource.ID)
		if err != nil {
			return err
		}
		err = az.appendRoutes(resource, resourceGroupID)
		if err != nil {
			return err
		}
	}

	filters, err := az.listRouteFilters()
	if err != nil {
		return err
	}
	for _, resource := range filters {
		az.appendRouteFilters(resource)
	}
	return nil
}
