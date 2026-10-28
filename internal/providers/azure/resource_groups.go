// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources/v4"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// ResourceGroupLister lists resource groups as azurerm_resource_group:
// the --resource-group one alone when it is set, else every group in the
// subscription. Resource Graph only finds groups that hold resources, so
// this lister is also the way to import empty ones.
type ResourceGroupLister struct {
	Service
}

// List lists the resource groups. A named group that can't be read
// (it doesn't exist, say) is an error, since the user asked for it. When
// listing the whole subscription a page that fails before any group is
// listed is an error; one that fails later keeps the groups listed before
// it, and the listing fails in part (see harvest.Partial), so that the
// groups it left out aren't taken for deleted.
func (az *ResourceGroupLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return az.Run(ctx, s, az.list)
}

func (az *ResourceGroupLister) list() error {
	subscriptionID, resourceGroup, credential, options := az.clientSettings()
	client, err := armresources.NewResourceGroupsClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := az.Context()
	var groups []*armresources.ResourceGroup
	if resourceGroup != "" {
		var got armresources.ResourceGroupsClientGetResponse
		got, err = client.Get(ctx, resourceGroup, nil)
		if err != nil {
			return err
		}
		groups = []*armresources.ResourceGroup{&got.ResourceGroup}
	} else {
		groups, err = listAll(ctx, client.NewListPager(nil),
			func(p armresources.ResourceGroupsClientListResponse) []*armresources.ResourceGroup {
				return p.Value
			})
		if err != nil && len(groups) == 0 {
			return err
		}
	}
	for _, group := range groups {
		if group.ID == nil || group.Name == nil {
			continue
		}
		az.recordTagged(*group.ID, *group.Name, "azurerm_resource_group", group.Tags)
	}
	if err != nil {
		return harvest.Partial(fmt.Errorf("azurerm: resource_group: listed %d resource groups before a page of the list failed: %w", len(groups), err))
	}
	return nil
}
