// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The identities of the subscription, or of --resource-group, are listed
// with their federated credentials, asked for under each identity's own
// resource group. Credentials that can't be listed leave the identity
// listed and fail the listing in part, naming it; those of an identity gone
// since it was listed (a 404) leave nothing out.
func TestManagedIdentityLister(t *testing.T) {
	t.Parallel()
	const (
		scoped = "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.ManagedIdentity/userAssignedIdentities"
		all    = "/subscriptions/sub/providers/Microsoft.ManagedIdentity/userAssignedIdentities"
		other  = "/subscriptions/sub/resourceGroups/rg2/providers/Microsoft.ManagedIdentity/userAssignedIdentities"
		deploy = other + "/deploy"
		app    = other + "/app"
	)
	credentials := func(identity string) string { return identity + "/federatedIdentityCredentials" }
	identities := armList(armItem(deploy, "deploy", ""), armItem(app, "app", ""))
	deployCredential := listedResource{Type: "azurerm_federated_identity_credential", ID: credentials(deploy) + "/github", Name: "deploy_github"}
	appCredential := listedResource{Type: "azurerm_federated_identity_credential", ID: credentials(app) + "/aks", Name: "app_aks"}
	for _, tc := range []struct {
		name          string
		resourceGroup string
		bodies        map[string]string
		refused       map[string]bool
		want          []listedResource
		wantPaths     []string
		// partial is the identity the listing must fail in part naming,
		// "" for a listing in full.
		partial string
	}{
		{
			name: "the subscription",
			bodies: map[string]string{
				all:                 identities,
				credentials(deploy): armList(armItem(credentials(deploy)+"/github", "github", "")),
				credentials(app):    armList(armItem(credentials(app)+"/aks", "aks", "")),
			},
			want: []listedResource{
				{Type: "azurerm_user_assigned_identity", ID: deploy, Name: "deploy"},
				deployCredential,
				{Type: "azurerm_user_assigned_identity", ID: app, Name: "app"},
				appCredential,
			},
			wantPaths: []string{all, credentials(deploy), credentials(app)},
		},
		{
			name:          "a resource group",
			resourceGroup: "rg1",
			bodies: map[string]string{
				scoped: armList(armItem(scoped+"/ci", "ci", "")),
				scoped + "/ci/federatedIdentityCredentials": armList(),
			},
			want:      []listedResource{{Type: "azurerm_user_assigned_identity", ID: scoped + "/ci", Name: "ci"}},
			wantPaths: []string{scoped, scoped + "/ci/federatedIdentityCredentials"},
		},
		{
			name: "credentials refused",
			bodies: map[string]string{
				all:              identities,
				credentials(app): armList(armItem(credentials(app)+"/aks", "aks", "")),
			},
			refused: map[string]bool{credentials(deploy): true},
			want: []listedResource{
				{Type: "azurerm_user_assigned_identity", ID: deploy, Name: "deploy"},
				{Type: "azurerm_user_assigned_identity", ID: app, Name: "app"},
				appCredential,
			},
			wantPaths: []string{all, credentials(deploy), credentials(app)},
			partial:   deploy,
		},
		{
			name: "an identity gone",
			bodies: map[string]string{
				all:              identities,
				credentials(app): armList(armItem(credentials(app)+"/aks", "aks", "")),
			},
			want: []listedResource{
				{Type: "azurerm_user_assigned_identity", ID: deploy, Name: "deploy"},
				{Type: "azurerm_user_assigned_identity", ID: app, Name: "app"},
				appCredential,
			},
			wantPaths: []string{all, credentials(deploy), credentials(app)},
		},
	} {
		transport := &routedTransport{bodies: tc.bodies, refused: tc.refused}
		lister := &ManagedIdentityLister{}
		lister.Configure(harvest.ListerConfig{Settings: routedArgs(transport, tc.resourceGroup)})

		err := lister.List()
		switch {
		case tc.partial != "":
			if !harvest.IsPartial(err) || !strings.Contains(err.Error(), "federated credentials of "+tc.partial) {
				t.Errorf("%s: got %v, want a partial error naming %s", tc.name, err, tc.partial)
			}
		case err != nil:
			t.Errorf("%s: %v", tc.name, err)
		}
		if got := listedResources(lister.Listed()); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: listed %+v, want %+v", tc.name, got, tc.want)
		}
		if !reflect.DeepEqual(transport.paths, tc.wantPaths) {
			t.Errorf("%s: requested %v, want %v", tc.name, transport.paths, tc.wantPaths)
		}
	}
}
