// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// ManagedIdentityLister lists user-assigned identities, in the
// subscription or in --resource-group, and their federated identity
// credentials. An identity whose credentials can't be listed is listed
// without them, or with those listed before the list failed, and the
// listing fails in part (see harvest.Partial), so that the credentials it
// left out aren't taken for deleted. An identity gone since it was listed
// has no credentials to leave out.
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
	var failures []error
	for _, identity := range listed {
		if identity.ID == nil || identity.Name == nil {
			continue
		}
		az.recordTagged(*identity.ID, *identity.Name, "azurerm_user_assigned_identity", identity.Tags)
		// A call per identity, for credentials the import may not keep.
		if !az.wants("azurerm_federated_identity_credential") {
			continue
		}
		// Federated credentials are listed under the identity's own resource
		// group, which is not --resource-group when the whole subscription is
		// listed.
		identityResourceGroup, err := resourceGroupOf(*identity.ID)
		if err != nil {
			failures = append(failures, fmt.Errorf("azurerm: managed_identity: federated credentials of %s: %w", *identity.ID, err))
			continue
		}
		federated, err := listAll(ctx, credentials.NewListPager(identityResourceGroup, *identity.Name, nil),
			func(p armmsi.FederatedIdentityCredentialsClientListResponse) []*armmsi.FederatedIdentityCredential {
				return p.Value
			})
		for _, c := range federated {
			if c.ID != nil && c.Name != nil {
				az.recordBare(*c.ID, *identity.Name+"_"+*c.Name, "azurerm_federated_identity_credential")
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var respErr *azcore.ResponseError
			if errors.As(err, &respErr) && respErr.StatusCode == http.StatusNotFound {
				log.Printf("azurerm: managed_identity: %s is gone: %v", *identity.ID, err)
				continue
			}
			failures = append(failures, fmt.Errorf("azurerm: managed_identity: listing the federated credentials of %s: %w", *identity.ID, err))
		}
	}
	return harvest.Partial(errors.Join(failures...))
}
