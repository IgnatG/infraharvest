# azurerm_role_assignment, from data/azurerm/azurerm.yaml

import {
  to = azurerm_role_assignment.app_contributor
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app/providers/Microsoft.Authorization/roleAssignments/33333333-3333-3333-3333-333333333333"
}

import {
  to = azurerm_role_assignment.app_reader
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app/providers/Microsoft.Authorization/roleAssignments/11111111-1111-1111-1111-111111111111"
}

import {
  to = azurerm_role_assignment.subscription_app_deployer
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/providers/Microsoft.Authorization/roleAssignments/22222222-2222-2222-2222-222222222222"
}

# /providers/Microsoft.Management/managementGroups/platform/providers/Microsoft.Authorization/roleAssignments/44444444-4444-4444-4444-444444444444: left out by default, would import as "/providers/Microsoft.Management/managementGroups/platform/providers/Microsoft.Authorization/roleAssignments/44444444-4444-4444-4444-444444444444": assigned at a management group, the tenant root or another subscription, and inherited by this one; import it with what manages that scope
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/MC_rg-aks_aks_uksouth/providers/Microsoft.Authorization/roleAssignments/55555555-5555-5555-5555-555555555555: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/MC_rg-aks_aks_uksouth/providers/Microsoft.Authorization/roleAssignments/55555555-5555-5555-5555-555555555555": in a resource group another resource manages, such as an AKS cluster's node resource group (MC_*); that resource creates and deletes what is in it

# Left out of generated configuration: role_definition_id when !@role_definition_listed, role_definition_name when @role_definition_listed, principal_type=ForeignGroup, principal_type=Device

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app/providers/Microsoft.Authorization/roleAssignments/11111111-1111-1111-1111-111111111111: also left out, from what its lister recorded: role_definition_id
# /subscriptions/00000000-0000-0000-0000-000000000001/providers/Microsoft.Authorization/roleAssignments/22222222-2222-2222-2222-222222222222: also left out, from what its lister recorded: role_definition_name
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app/providers/Microsoft.Authorization/roleAssignments/33333333-3333-3333-3333-333333333333: also left out, from what its lister recorded: role_definition_id
# /providers/Microsoft.Management/managementGroups/platform/providers/Microsoft.Authorization/roleAssignments/44444444-4444-4444-4444-444444444444: also left out, from what its lister recorded: role_definition_id
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/MC_rg-aks_aks_uksouth/providers/Microsoft.Authorization/roleAssignments/55555555-5555-5555-5555-555555555555: also left out, from what its lister recorded: role_definition_id

# Generated configuration, without the arguments the catalogue omits:
resource "azurerm_role_assignment" "app_reader" {
  name                 = "11111111-1111-1111-1111-111111111111"
  principal_id         = "aaaaaaaa-0000-0000-0000-000000000001"
  principal_type       = "ServicePrincipal"
  role_definition_name = "Reader"
  scope                = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app"
}

resource "azurerm_role_assignment" "subscription_app_deployer" {
  name               = "22222222-2222-2222-2222-222222222222"
  principal_id       = "aaaaaaaa-0000-0000-0000-000000000002"
  principal_type     = "Group"
  role_definition_id = "/subscriptions/00000000-0000-0000-0000-000000000001/providers/Microsoft.Authorization/roleDefinitions/bbbbbbbb-0000-0000-0000-000000000001"
  scope              = "/subscriptions/00000000-0000-0000-0000-000000000001"
}

resource "azurerm_role_assignment" "app_contributor" {
  name                 = "33333333-3333-3333-3333-333333333333"
  principal_id         = "aaaaaaaa-0000-0000-0000-000000000003"
  role_definition_name = "Contributor"
  scope                = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app"
}
