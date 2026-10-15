// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"log"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
)

// ManagedIdentityLister lists user-assigned identities, in the
// subscription or in --resource-group, and their federated identity
// credentials. An identity whose credentials can't be listed is logged
// and listed without them.
type ManagedIdentityLister struct {
	Service
}

func (az *ManagedIdentityLister) List() error {
	subscriptionID, resourceGroup, credential, options := az.clientSettings()
	identities, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	credentials, err := armmsi.NewFederatedIdentityCredentialsClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	ctx := az.Context()
	var listed []*armmsi.Identity
	if resourceGroup != "" {
		listed, err = listAll(ctx, identities.NewListByResourceGroupPager(resourceGroup, nil),
			func(p armmsi.UserAssignedIdentitiesClientListByResourceGroupResponse) []*armmsi.Identity {
				return p.Value
			})
	} else {
		listed, err = listAll(ctx, identities.NewListBySubscriptionPager(nil),
			func(p armmsi.UserAssignedIdentitiesClientListBySubscriptionResponse) []*armmsi.Identity {
				return p.Value
			})
	}
	if err != nil {
		return err
	}
	for _, identity := range listed {
		if identity.ID == nil || identity.Name == nil {
			continue
		}
		az.recordBare(*identity.ID, *identity.Name, "azurerm_user_assigned_identity")
		// Federated credentials are listed under the identity's own resource
		// group, which is not --resource-group when the whole subscription is
		// listed.
		identityResourceGroup, err := resourceGroupOf(*identity.ID)
		if err != nil {
			log.Printf("azurerm: managed_identity: %s: %v", *identity.ID, err)
			continue
		}
		federated, err := listAll(ctx, credentials.NewListPager(identityResourceGroup, *identity.Name, nil),
			func(p armmsi.FederatedIdentityCredentialsClientListResponse) []*armmsi.FederatedIdentityCredential {
				return p.Value
			})
		if err != nil {
			log.Printf("azurerm: managed_identity: federated credentials of %s: %v", *identity.ID, err)
			continue
		}
		for _, c := range federated {
			if c.ID != nil && c.Name != nil {
				az.recordBare(*c.ID, *identity.Name+"_"+*c.Name, "azurerm_federated_identity_credential")
			}
		}
	}
	return nil
}
