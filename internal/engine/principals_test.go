// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"path/filepath"
	"strings"
	"testing"
)

// The subscription of the role assignment tests, and its custom role.
const (
	roleSubscription = "/subscriptions/00000000-0000-0000-0000-000000000001"
	deployerRole     = roleSubscription + "/providers/Microsoft.Authorization/roleDefinitions/bbbbbbbb-0000-0000-0000-000000000001"
)

// A role assignment refers to the identity it grants a role to, a
// user-assigned identity's principal or a resource's system-assigned
// one, to the custom role it assigns by the role's resource ID, and to
// the resource group it is on. A principal two resources have, or none,
// stays as written.
func TestAddReferencesOfRoleAssignments(t *testing.T) {
	t.Parallel()
	values := map[string]map[string]any{
		"azurerm_resource_group.app":          {"id": roleSubscription + "/resourceGroups/app", "name": "app", "location": "uksouth"},
		"azurerm_user_assigned_identity.ci":   {"id": roleSubscription + "/resourceGroups/app/providers/Microsoft.ManagedIdentity/userAssignedIdentities/ci", "name": "ci", "principal_id": "aaaaaaaa-0000-0000-0000-000000000001", "client_id": "cccccccc-0000-0000-0000-000000000001", "tenant_id": "tttttttt-0000-0000-0000-000000000001"},
		"azurerm_linux_web_app.web":           {"id": roleSubscription + "/resourceGroups/app/providers/Microsoft.Web/sites/web", "name": "web", "identity": []any{map[string]any{"type": "SystemAssigned", "principal_id": "aaaaaaaa-0000-0000-0000-000000000002", "tenant_id": "tttttttt-0000-0000-0000-000000000001"}}},
		"azurerm_linux_web_app.one":           {"id": roleSubscription + "/resourceGroups/app/providers/Microsoft.Web/sites/one", "name": "one", "identity": []any{map[string]any{"principal_id": "aaaaaaaa-0000-0000-0000-000000000004"}}},
		"azurerm_windows_web_app.two":         {"id": roleSubscription + "/resourceGroups/app/providers/Microsoft.Web/sites/two", "name": "two", "identity": []any{map[string]any{"principal_id": "aaaaaaaa-0000-0000-0000-000000000004"}}},
		"azurerm_linux_web_app.user_assigned": {"id": roleSubscription + "/resourceGroups/app/providers/Microsoft.Web/sites/ua", "name": "ua", "identity": []any{map[string]any{"type": "UserAssigned", "principal_id": ""}}},
		"azurerm_role_definition.deployer":    {"id": deployerRole + "|" + roleSubscription, "name": "App Deployer", "role_definition_id": "bbbbbbbb-0000-0000-0000-000000000001", "role_definition_resource_id": deployerRole, "scope": roleSubscription},
		"azurerm_role_assignment.ci":          {"id": roleSubscription + "/resourceGroups/app/providers/Microsoft.Authorization/roleAssignments/1", "principal_id": "aaaaaaaa-0000-0000-0000-000000000001"},
	}
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "azurerm_role_assignment" "ci" {
  principal_id       = "aaaaaaaa-0000-0000-0000-000000000001"
  role_definition_id = "`+deployerRole+`"
  scope              = "`+roleSubscription+`/resourceGroups/app"
}

resource "azurerm_role_assignment" "web" {
  principal_id         = "aaaaaaaa-0000-0000-0000-000000000002"
  role_definition_name = "Reader"
  scope                = "`+roleSubscription+`/resourceGroups/app"
}

resource "azurerm_role_assignment" "both" {
  principal_id         = "aaaaaaaa-0000-0000-0000-000000000004"
  role_definition_name = "Reader"
  scope                = "`+roleSubscription+`/resourceGroups/app"
}

resource "azurerm_role_assignment" "group" {
  principal_id         = "aaaaaaaa-0000-0000-0000-000000000003"
  role_definition_name = "Reader"
  scope                = "`+roleSubscription+`/resourceGroups/app"
}
`)
	if changed, err := addReferences(dir, values); err != nil || !changed {
		t.Fatalf("want references, got changed=%v err=%v", changed, err)
	}
	got := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{
		"principal_id = azurerm_user_assigned_identity.ci.principal_id",
		"role_definition_id = azurerm_role_definition.deployer.role_definition_resource_id",
		"principal_id = azurerm_linux_web_app.web.identity[0].principal_id",
		"scope = azurerm_resource_group.app.id",
		// Two resources have the principal: no telling which.
		`principal_id = "aaaaaaaa-0000-0000-0000-000000000004"`,
		// No resource has it: an Entra ID user, group or application.
		`principal_id = "aaaaaaaa-0000-0000-0000-000000000003"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// A role assignment's and a role definition's scope that is the
// subscription the provider signs in to read it; a resource group's
// stays.
func TestAddLookupsOfTheSubscription(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "azurerm_role_definition" "deployer" {
  assignable_scopes = ["`+roleSubscription+`"]
  name              = "App Deployer"
  scope             = "`+roleSubscription+`"
}

resource "azurerm_role_assignment" "app" {
  scope = "`+roleSubscription+`/resourceGroups/app"
}
`)
	edited, err := addLookups(dir, Names{}, nil)
	if err != nil || !edited["azurerm_role_definition.deployer"] || len(edited) != 1 {
		t.Fatalf("want the role definition edited, got %v, %v", edited, err)
	}
	got := squashed(readFile(t, dir, GeneratedFileName))
	for _, want := range []string{
		"assignable_scopes = [data.azurerm_subscription.current.id]",
		"scope = data.azurerm_subscription.current.id",
		`scope = "` + roleSubscription + `/resourceGroups/app"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if data := squashed(readFile(t, dir, DataFileName)); strings.Count(data, `data "azurerm_subscription" "current" {`) != 1 {
		t.Errorf("data.tf:\n%s", data)
	}
}

// A role assignment's principal_id that is still a literal is noted as an
// Entra ID object, of the kind its principal_type says, once; one that
// refers to an identity isn't, nor is another type's.
func TestNotePrincipals(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, `resource "azurerm_role_assignment" "group" {
  principal_id         = "aaaaaaaa-0000-0000-0000-000000000003"
  principal_type       = "Group"
  role_definition_name = "Reader"
}

resource "azurerm_role_assignment" "foreign" {
  principal_id = "aaaaaaaa-0000-0000-0000-000000000005"
}

resource "azurerm_role_assignment" "ci" {
  principal_id = azurerm_user_assigned_identity.ci.principal_id
}

resource "azurerm_key_vault_access_policy" "other" {
  object_id = "aaaaaaaa-0000-0000-0000-000000000003"
}
`)
	path := filepath.Join(dir, GeneratedFileName)
	if noted, err := notePrincipals(path); err != nil || !noted {
		t.Fatalf("want notes, got %v, %v", noted, err)
	}
	got := readFile(t, dir, GeneratedFileName)
	if _, err := loadHCL(path); err != nil {
		t.Fatalf("the noted file doesn't parse: %v\n%s", err, got)
	}
	for _, want := range []string{
		`principal_id         = "aaaaaaaa-0000-0000-0000-000000000003" # Entra ID group: users, groups and applications belong to the azuread provider` + "\n",
		`principal_id = "aaaaaaaa-0000-0000-0000-000000000005" # Entra ID object: users, groups and applications belong to the azuread provider` + "\n",
		"principal_id = azurerm_user_assigned_identity.ci.principal_id\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Count(got, "# Entra ID ") != 2 {
		t.Errorf("want two notes:\n%s", got)
	}
	if noted, err := notePrincipals(path); err != nil || noted {
		t.Errorf("noted again: %v, %v\n%s", noted, err, readFile(t, dir, GeneratedFileName))
	}
}
