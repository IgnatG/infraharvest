// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"reflect"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// Locks are listed in --resource-group when it is set, else in the whole
// subscription; a lock without a name is skipped. A lock in a resource
// group records what manages the group, from one Resource Graph query of
// the run's groups, so that the catalogue leaves out the locks in an AKS
// cluster's node resource group and keeps the others.
func TestManagementLockListsTheScope(t *testing.T) {
	t.Parallel()
	const (
		graphPath = "/providers/Microsoft.ResourceGraph/resources"
		keep      = roleSubscription + "/resourceGroups/rg1/providers/Microsoft.Authorization/locks/keep"
		nodes     = roleSubscription + "/resourceGroups/MC_rg-aks_aks_uksouth/providers/Microsoft.Network/loadBalancers/kubernetes/providers/Microsoft.Authorization/locks/nodes"
	)
	groups := graphRows(
		`{"id":"`+roleSubscription+`/resourceGroups/rg1","tags":{},"managedBy":""}`,
		`{"id":"`+roleSubscription+`/resourceGroups/MC_rg-aks_aks_uksouth","tags":{},"managedBy":"`+aksCluster+`"}`,
	)
	keepLock := listedResource{Type: "azurerm_management_lock", ID: keep, Name: "keep", Attributes: map[string]string{"name": "keep", "resource_group_name": "rg1"}}
	nodesLock := listedResource{Type: "azurerm_management_lock", ID: nodes, Name: "nodes", Attributes: map[string]string{
		"name": "nodes", "resource_group_name": "MC_rg-aks_aks_uksouth", "resource_group_managed_by": aksCluster,
	}}
	for _, tc := range []struct {
		resourceGroup, path string
		want                []listedResource
	}{
		{"rg1", roleSubscription + "/resourceGroups/rg1/providers/Microsoft.Authorization/locks", []listedResource{keepLock}},
		{"", roleSubscription + "/providers/Microsoft.Authorization/locks", []listedResource{keepLock, nodesLock}},
	} {
		items := []string{armItem(keep, "keep", `{"level":"CanNotDelete"}`), `{"id":"` + keep + `2"}`}
		if tc.resourceGroup == "" {
			items = append(items, armItem(nodes, "nodes", `{"level":"CanNotDelete"}`))
		}
		transport := &routedTransport{bodies: map[string]string{tc.path: armList(items...), graphPath: groups}}
		g := &ManagementLockLister{}
		g.SetSettings(serviceSettings{
			subscriptionID: roleSubscriptionID,
			resourceGroup:  tc.resourceGroup,
			credential:     fakeCredential{},
			clientOptions:  &arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: transport}},
		})

		if err := g.List(); err != nil {
			t.Fatalf("resource group %q: %v", tc.resourceGroup, err)
		}

		if want := []string{tc.path, graphPath}; !reflect.DeepEqual(transport.paths, want) {
			t.Errorf("resource group %q: requested %v, want %v", tc.resourceGroup, transport.paths, want)
		}
		if got := listedResources(g.Resources); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("resource group %q: got %+v, want %+v", tc.resourceGroup, got, tc.want)
		}
	}
}

// Locks on the subscription alone are in no resource group, so nothing
// asks Resource Graph what manages one.
func TestManagementLockSkipsTheGroupsQueryWithoutGroups(t *testing.T) {
	t.Parallel()
	const (
		path = roleSubscription + "/providers/Microsoft.Authorization/locks"
		lock = path + "/all"
	)
	transport := &routedTransport{bodies: map[string]string{path: armList(armItem(lock, "all", `{"level":"ReadOnly"}`))}}
	g := &ManagementLockLister{}
	g.SetSettings(serviceSettings{
		subscriptionID: roleSubscriptionID,
		credential:     fakeCredential{},
		clientOptions:  &arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: transport}},
	})

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(transport.paths, []string{path}) {
		t.Errorf("requested %v, want only %s", transport.paths, path)
	}
	want := []listedResource{{Type: "azurerm_management_lock", ID: lock, Name: "all", Attributes: map[string]string{"name": "all"}}}
	if got := listedResources(g.Resources); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
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
		lister  func() settledLister
		path    string
		body    string
		want    []listedResource
	}{
		{
			service: "security_center_contact",
			lister:  func() settledLister { return &SecurityCenterContactLister{} },
			path:    contacts,
			body:    armList(armItem(contacts+"/default", "default", `{"emails":"secops@example.com"}`), `{"name":"nameless"}`),
			want:    []listedResource{{Type: "azurerm_security_center_contact", ID: contacts + "/default", Name: "default"}},
		},
		{
			service: "security_center_subscription_pricing",
			lister:  func() settledLister { return &SecurityCenterSubscriptionPricingLister{} },
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
		g.SetSettings(routedArgs(transport, ""))
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
		g.SetSettings(routedArgs(transport, "rg1"))
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
	for service, g := range map[string]settledLister{
		"management_lock":                      &ManagementLockLister{},
		"security_center_contact":              &SecurityCenterContactLister{},
		"security_center_subscription_pricing": &SecurityCenterSubscriptionPricingLister{},
	} {
		// Nothing registered: every request gets a 404.
		g.SetSettings(routedArgs(&routedTransport{}, ""))
		if err := g.List(); err == nil {
			t.Errorf("%s: listed %v, want an error", service, g.Listed())
		}
	}
}
