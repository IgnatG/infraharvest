// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armlocks"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// ManagementLockLister lists management locks as azurerm_management_lock:
// those in --resource-group when it is set, else every lock in the
// subscription. Locks are extension resources (Microsoft.Authorization/locks)
// that Resource Graph doesn't index, so resource_graph can't list them.
// Each records its name, the resource group of what it locks (see
// lockAttributes) and what manages that group (see withGroupManagers, asked
// only when a lock is in a group), so that the catalogue's rules leave out
// the locks Azure Backup adds and those on what is in a group they leave
// out, such as a DefaultResourceGroup-<region> or an AKS cluster's MC_*
// node resource group.
type ManagementLockLister struct {
	Service
}

// List lists the locks. Resource Manager's list at a scope also
// returns the locks on the resources under it, so a group's list includes
// the locks on its resources, and the subscription's list every lock.
func (az *ManagementLockLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return az.Run(ctx, s, az.list)
}

func (az *ManagementLockLister) list() error {
	subscriptionID, resourceGroup, credential, options := az.clientSettings()
	predicate, err := graphPredicate(subscriptionID, resourceGroup)
	if err != nil {
		return err
	}
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
	var listed []harvest.Resource
	for _, lock := range locks {
		if lock.ID == nil || lock.Name == nil {
			continue
		}
		listed = append(listed, harvest.Record(*lock.ID, *lock.Name, "azurerm_management_lock", az.Provider(), lockAttributes(*lock.ID, *lock.Name)))
	}
	if err := withGroupManagers(ctx, credential, options, subscriptionID, predicate, listed); err != nil {
		return err
	}
	az.Resources = append(az.Resources, listed...)
	return nil
}

// lockAttributes records a lock's name, and the resource group its ID is
// under, the lock's scope's, as resource_group_name: none for a lock on
// the subscription.
func lockAttributes(id, name string) map[string]string {
	attributes := map[string]string{"name": name}
	segments := strings.Split(id, "/")
	for i := 0; i+1 < len(segments); i++ {
		if strings.EqualFold(segments[i], "resourceGroups") && segments[i+1] != "" {
			attributes["resource_group_name"] = segments[i+1]
			break
		}
	}
	return attributes
}
