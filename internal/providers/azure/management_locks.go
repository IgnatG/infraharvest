// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armlocks"
)

// ManagementLockLister lists management locks as azurerm_management_lock:
// those in --resource-group when it is set, else every lock in the
// subscription. Locks are extension resources (Microsoft.Authorization/locks)
// that Resource Graph doesn't index, so resource_graph can't list them.
type ManagementLockLister struct {
	Service
}

// List lists the locks. Resource Manager's list at a scope also
// returns the locks on the resources under it, so a group's list includes
// the locks on its resources, and the subscription's list every lock.
func (az *ManagementLockLister) List() error {
	subscriptionID, resourceGroup, credential, options := az.clientSettings()
	client, err := armlocks.NewManagementLocksClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := az.Context()
	var locks []*armlocks.ManagementLockObject
	if resourceGroup != "" {
		locks, err = listAll(ctx, client.NewListAtResourceGroupLevelPager(resourceGroup, nil),
			func(p armlocks.ManagementLocksClientListAtResourceGroupLevelResponse) []*armlocks.ManagementLockObject {
				return p.Value
			})
	} else {
		locks, err = listAll(ctx, client.NewListAtSubscriptionLevelPager(nil),
			func(p armlocks.ManagementLocksClientListAtSubscriptionLevelResponse) []*armlocks.ManagementLockObject {
				return p.Value
			})
	}
	if err != nil {
		return err
	}
	for _, lock := range locks {
		if lock.ID == nil || lock.Name == nil {
			continue
		}
		az.recordBare(*lock.ID, *lock.Name, "azurerm_management_lock")
	}
	return nil
}
