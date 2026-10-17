// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources/v4"
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
// listing the whole subscription only a failure on the first page is an
// error; a later page that fails is logged and the groups read so far kept.
func (az *ResourceGroupLister) List() error {
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
		groups, err = listAllLenient(ctx, client.NewListPager(nil),
			func(p armresources.ResourceGroupsClientListResponse) []*armresources.ResourceGroup {
				return p.Value
			})
		if err != nil {
			return err
		}
	}
	for _, group := range groups {
		if group.ID == nil || group.Name == nil {
			continue
		}
		az.recordTagged(*group.ID, *group.Name, "azurerm_resource_group", group.Tags)
	}
	return nil
}
