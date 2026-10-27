// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"reflect"
	"strings"
	"testing"
)

const identityCluster = `resource "azurerm_user_assigned_identity" "ci" {
  isolation_scope     = null
  location            = "westeurope"
  name                = "ci"
  resource_group_name = azurerm_resource_group.app.name
  tags                = { team = "build" }
}
resource "azurerm_federated_identity_credential" "ci_github" {
  audience            = ["api://AzureADTokenExchange"]
  issuer              = "https://token.actions.githubusercontent.com"
  name                = "github"
  parent_id           = azurerm_user_assigned_identity.ci.id
  resource_group_name = azurerm_resource_group.app.name
  subject             = "repo:acme/app:ref:refs/heads/main"
}
`

func TestAzureUserAssignedIdentityMaps(t *testing.T) {
	t.Parallel()
	call, err := azureUserAssignedIdentity.Map(cluster(t, identityCluster))
	if err != nil {
		t.Fatal(err)
	}
	got := render(t, azureUserAssignedIdentity, call)
	for _, want := range []string{
		`name = "ci"`,
		`location = "westeurope"`,
		"resource_group_name = azurerm_resource_group.app.name",
		`tags = { team = "build" }`,
		`federated_identity_credentials = { "github" = { name = "github" audience = ["api://AzureADTokenExchange"] issuer = "https://token.actions.githubusercontent.com" subject = "repo:acme/app:ref:refs/heads/main" } }`,
		"enable_telemetry = false",
	} {
		if !strings.Contains(got, squash(want)) {
			t.Errorf("call misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "isolation_scope") {
		t.Errorf("a null argument is set:\n%s", got)
	}
	wantAddresses := map[string]string{
		"azurerm_user_assigned_identity.ci":               "azurerm_user_assigned_identity.this",
		"azurerm_federated_identity_credential.ci_github": `azurerm_federated_identity_credential.this["github"]`,
	}
	if !reflect.DeepEqual(call.Addresses, wantAddresses) {
		t.Errorf("addresses: %v", call.Addresses)
	}
	if call.Outputs["azurerm_user_assigned_identity.ci"]["principal_id"] != "principal_id" {
		t.Errorf("outputs: %v", call.Outputs)
	}
}

func TestAzureUserAssignedIdentityDeclines(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ from, to string }{
		"no location":         {`location            = "westeurope"`, ""},
		"a name not literal":  {`name                = "github"`, "name = local.credential"},
		"another parent":      {"parent_id           = azurerm_user_assigned_identity.ci.id", "parent_id = azurerm_user_assigned_identity.other.id"},
		"an unknown argument": {`tags                = { team = "build" }`, "tags = { team = \"build\" }\n  timeouts { create = \"5m\" }"},
	} {
		src := strings.Replace(identityCluster, tc.from, tc.to, 1)
		if src == identityCluster {
			t.Fatalf("%s: the replacement doesn't apply", name)
		}
		if _, err := azureUserAssignedIdentity.Map(cluster(t, src)); !IsDecline(err) {
			t.Errorf("%s: got %v, want a decline", name, err)
		}
	}
}

// azurerm 4 names a credential's identity by user_assigned_identity_id.
func TestAzureUserAssignedIdentityMapsTheNewParent(t *testing.T) {
	t.Parallel()
	src := strings.Replace(identityCluster, "parent_id           = azurerm_user_assigned_identity.ci.id", "user_assigned_identity_id = azurerm_user_assigned_identity.ci.id", 1)
	src = strings.Replace(src, "  resource_group_name = azurerm_resource_group.app.name\n  subject", "  subject", 1)
	if src == identityCluster {
		t.Fatal("the replacement doesn't apply")
	}
	call, err := azureUserAssignedIdentity.Map(cluster(t, src))
	if err != nil {
		t.Fatal(err)
	}
	if call.Addresses["azurerm_federated_identity_credential.ci_github"] != `azurerm_federated_identity_credential.this["github"]` {
		t.Errorf("addresses: %v", call.Addresses)
	}
}
