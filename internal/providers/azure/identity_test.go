// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"reflect"
	"testing"
)

const (
	testTenant = "11111111-1111-1111-1111-111111111111"
	testClient = "22222222-2222-2222-2222-222222222222"
)

// The key of settings, which names the inventory of a call, has the
// identity after the resource group, the subscription and the provider
// type, which it pads; without an identity it is as it was without one.
func TestSettingsKeyHasTheIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		settings Settings
		want     []string
	}{
		{Settings{ResourceGroup: "rg", Subscription: "sub", TenantID: testTenant}, []string{"rg", "sub", "", "tenant_id=" + testTenant}},
		{Settings{Subscription: "sub", AzAPI: true, ClientID: testClient}, []string{"", "sub", "azapi", "client_id=" + testClient}},
		{Settings{ResourceGroup: "rg", Subscription: "sub", TenantID: testTenant, ClientID: testClient}, []string{"rg", "sub", "", "tenant_id=" + testTenant, "client_id=" + testClient}},
		{Settings{ResourceGroup: "rg", Subscription: "sub"}, []string{"rg", "sub"}},
	} {
		if got := tc.settings.InventoryKey(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%+v: got %q, want %q", tc.settings, got, tc.want)
		}
	}
}

// Init signs a subscription in as the identity its environment names, in
// place of ARM_TENANT_ID and ARM_CLIENT_ID, and gives Terraform the same;
// a subscription without one signs in as the environment does.
func TestInitTakesTheIdentity(t *testing.T) {
	clearARMEnvironment(t)
	t.Setenv("ARM_TENANT_ID", "99999999-9999-9999-9999-999999999999")
	t.Setenv("ARM_CLIENT_ID", "88888888-8888-8888-8888-888888888888")
	p := &Provider{}
	if err := p.Init(t.Context(), Settings{Subscription: "sub-identity", TenantID: testTenant, ClientID: testClient}); err != nil {
		t.Fatal(err)
	}
	if p.tenantID != testTenant || p.clientID != testClient || p.subscriptionID != "sub-identity" || p.azapi {
		t.Errorf("tenant %q, client %q, subscription %q, azapi %v", p.tenantID, p.clientID, p.subscriptionID, p.azapi)
	}
	env, err := p.TerraformEnv(t.Context())
	if want := map[string]string{"ARM_TENANT_ID": testTenant, "ARM_CLIENT_ID": testClient}; err != nil || !reflect.DeepEqual(env, want) {
		t.Errorf("Terraform's environment: %v, %v", env, err)
	}
	// The identity is a sign-in of its own: its credential isn't the
	// environment's.
	other := &Provider{}
	if err := other.Init(t.Context(), Settings{Subscription: "sub-identity"}); err != nil {
		t.Fatal(err)
	}
	if other.tenantID != "" || other.credential == p.credential {
		t.Errorf("the environment's sign-in is shared: %q", other.tenantID)
	}
	if env, _ := other.TerraformEnv(t.Context()); len(env) != 0 {
		t.Errorf("Terraform's environment: %v", env)
	}
}
