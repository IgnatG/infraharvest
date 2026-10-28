// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/security/armsecurity"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// SecurityCenterContactLister lists the subscription's Microsoft Defender
// for Cloud security contacts as azurerm_security_center_contact. Contacts
// belong to the subscription, not to a resource group, so with
// --resource-group there is nothing to list. Resource Graph keeps them
// apart from the resources it indexes, so resource_graph doesn't list them.
type SecurityCenterContactLister struct {
	Service
}

// List lists the security contacts, unless --resource-group is set.
func (az *SecurityCenterContactLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return az.Run(ctx, s, az.list)
}

func (az *SecurityCenterContactLister) list() error {
	subscriptionID, resourceGroup, credential, options := az.clientSettings()
	if resourceGroup != "" {
		return nil
	}
	client, err := armsecurity.NewContactsClient(subscriptionID, credential, options)
	if err != nil {
		return err
	}
	contacts, err := listAll(az.Context(), client.NewListPager(nil),
		func(p armsecurity.ContactsClientListResponse) []*armsecurity.Contact { return p.Value })
	if err != nil {
		return err
	}
	for _, contact := range contacts {
		if contact.ID == nil || contact.Name == nil {
			continue
		}
		az.recordBare(*contact.ID, *contact.Name, "azurerm_security_center_contact")
	}
	return nil
}
