// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"reflect"
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Locks are listed in --resource-group when it is set, else in the whole
// subscription; a lock without a name is skipped.
func TestManagementLockListsTheScope(t *testing.T) {
	t.Parallel()
	const lock = "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Authorization/locks/keep"
	for resourceGroup, wantPath := range map[string]string{
		"rg1": "/subscriptions/sub/resourceGroups/rg1/providers/Microsoft.Authorization/locks",
		"":    "/subscriptions/sub/providers/Microsoft.Authorization/locks",
	} {
		transport := &routedTransport{bodies: map[string]string{
			wantPath: armList(armItem(lock, "keep", `{"level":"CanNotDelete"}`), `{"id":"`+lock+`2"}`),
		}}
		g := &ManagementLockLister{}
		g.Configure(harvest.ListerConfig{Settings: routedArgs(transport, resourceGroup)})

		if err := g.List(); err != nil {
			t.Fatalf("resource group %q: %v", resourceGroup, err)
		}

		if !reflect.DeepEqual(transport.paths, []string{wantPath}) {
			t.Errorf("resource group %q: requested %v, want %s", resourceGroup, transport.paths, wantPath)
		}
		want := []listedResource{{Type: "azurerm_management_lock", ID: lock, Name: "keep", Attributes: map[string]string{"name": "keep", "resource_group_name": "rg1"}}}
		if got := listedResources(g.Resources); !reflect.DeepEqual(got, want) {
			t.Errorf("resource group %q: got %+v, want %+v", resourceGroup, got, want)
		}
	}
}

// A lock records its name and the resource group of what it locks, so
// that the catalogue leaves out the lock Azure Backup puts on a storage
// account, and locks in groups the default selection leaves out.
func TestLockAttributes(t *testing.T) {
	t.Parallel()
	cat := catalogue.MustBuiltin("azurerm")
	for _, tc := range []struct {
		id, name string
		want     map[string]string
		skipped  bool
	}{
		{"/subscriptions/sub/providers/Microsoft.Authorization/locks/all", "all", map[string]string{"name": "all"}, false},
		{"/subscriptions/sub/resourcegroups/app/providers/Microsoft.Storage/storageAccounts/data/providers/Microsoft.Authorization/locks/AzureBackupProtectionLock", "AzureBackupProtectionLock", map[string]string{"name": "AzureBackupProtectionLock", "resource_group_name": "app"}, true},
		{"/subscriptions/sub/resourceGroups/DefaultResourceGroup-WEU/providers/Microsoft.OperationalInsights/workspaces/w/providers/Microsoft.Authorization/locks/keep", "keep", map[string]string{"name": "keep", "resource_group_name": "DefaultResourceGroup-WEU"}, true},
		{"/subscriptions/sub/resourceGroups/app/providers/Microsoft.Authorization/locks/keep", "keep", map[string]string{"name": "keep", "resource_group_name": "app"}, false},
	} {
		got := lockAttributes(tc.id, tc.name)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.id, got, tc.want)
		}
		if skipped := cat.Skipped("azurerm_management_lock", tc.id, got) != ""; skipped != tc.skipped {
			t.Errorf("%s: left out by default %v, want %v", tc.id, skipped, tc.skipped)
		}
	}
}

// Security contacts and Defender plans belong to the subscription: they
// are listed for the whole subscription, and with --resource-group nothing
// is asked for or listed.
func TestDefenderListersListTheSubscription(t *testing.T) {
	t.Parallel()
	const (
		contacts = "/subscriptions/sub/providers/Microsoft.Security/securityContacts"
		pricings = "/subscriptions/sub/providers/Microsoft.Security/pricings"
	)
	for _, tc := range []struct {
		service string
		lister  func() harvest.Lister
		path    string
		body    string
		want    []listedResource
	}{
		{
			service: "security_center_contact",
			lister:  func() harvest.Lister { return &SecurityCenterContactLister{} },
			path:    contacts,
			body:    armList(armItem(contacts+"/default", "default", `{"emails":"secops@example.com"}`), `{"name":"nameless"}`),
			want:    []listedResource{{Type: "azurerm_security_center_contact", ID: contacts + "/default", Name: "default"}},
		},
		{
			service: "security_center_subscription_pricing",
			lister:  func() harvest.Lister { return &SecurityCenterSubscriptionPricingLister{} },
			path:    pricings,
			body: armList(
				armItem(pricings+"/VirtualMachines", "VirtualMachines", `{"pricingTier":"Standard","subPlan":"P2","deprecated":false}`),
				armItem(pricings+"/StorageAccounts", "StorageAccounts", `{"pricingTier":"Free"}`),
				armItem(pricings+"/Dns", "Dns", `{"pricingTier":"Free","deprecated":true}`),
				`{"id":"`+pricings+`/Nameless"}`),
			want: []listedResource{
				{Type: "azurerm_security_center_subscription_pricing", ID: pricings + "/VirtualMachines", Name: "VirtualMachines", Attributes: map[string]string{"tier": "Standard", "subplan": "P2", "deprecated": "false"}},
				{Type: "azurerm_security_center_subscription_pricing", ID: pricings + "/StorageAccounts", Name: "StorageAccounts", Attributes: map[string]string{"tier": "Free"}},
				{Type: "azurerm_security_center_subscription_pricing", ID: pricings + "/Dns", Name: "Dns", Attributes: map[string]string{"tier": "Free", "deprecated": "true"}},
			},
		},
	} {
		transport := &routedTransport{bodies: map[string]string{tc.path: tc.body}}
		g := tc.lister()
		g.Configure(harvest.ListerConfig{Settings: routedArgs(transport, "")})
		if err := g.List(); err != nil {
			t.Fatalf("%s: %v", tc.service, err)
		}
		if !reflect.DeepEqual(transport.paths, []string{tc.path}) {
			t.Errorf("%s: requested %v, want %s", tc.service, transport.paths, tc.path)
		}
		if got := listedResources(g.Listed()); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v, want %+v", tc.service, got, tc.want)
		}

		transport = &routedTransport{bodies: map[string]string{tc.path: tc.body}}
		g = tc.lister()
		g.Configure(harvest.ListerConfig{Settings: routedArgs(transport, "rg1")})
		if err := g.List(); err != nil {
			t.Errorf("%s with a resource group: %v", tc.service, err)
		}
		if len(transport.paths) != 0 || len(g.Listed()) != 0 {
			t.Errorf("%s with a resource group: requested %v, listed %v", tc.service, transport.paths, g.Listed())
		}
	}
}

// A list that fails fails the service, rather than listing nothing.
func TestLockAndDefenderListersFailWhenTheListFails(t *testing.T) {
	t.Parallel()
	for service, g := range map[string]harvest.Lister{
		"management_lock":                      &ManagementLockLister{},
		"security_center_contact":              &SecurityCenterContactLister{},
		"security_center_subscription_pricing": &SecurityCenterSubscriptionPricingLister{},
	} {
		// Nothing registered: every request gets a 404.
		g.Configure(harvest.ListerConfig{Settings: routedArgs(&routedTransport{}, "")})
		if err := g.List(); err == nil {
			t.Errorf("%s: listed %v, want an error", service, g.Listed())
		}
	}
}
