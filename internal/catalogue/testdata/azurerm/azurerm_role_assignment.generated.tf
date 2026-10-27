resource "azurerm_role_assignment" "app_reader" {
  name                 = "11111111-1111-1111-1111-111111111111"
  principal_id         = "aaaaaaaa-0000-0000-0000-000000000001"
  principal_type       = "ServicePrincipal"
  role_definition_id   = "/subscriptions/00000000-0000-0000-0000-000000000001/providers/Microsoft.Authorization/roleDefinitions/acdd72a7-3385-48ef-bd42-f606fba81ae7"
  role_definition_name = "Reader"
  scope                = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app"
}

resource "azurerm_role_assignment" "subscription_app_deployer" {
  name                 = "22222222-2222-2222-2222-222222222222"
  principal_id         = "aaaaaaaa-0000-0000-0000-000000000002"
  principal_type       = "Group"
  role_definition_id   = "/subscriptions/00000000-0000-0000-0000-000000000001/providers/Microsoft.Authorization/roleDefinitions/bbbbbbbb-0000-0000-0000-000000000001"
  role_definition_name = "App Deployer"
  scope                = "/subscriptions/00000000-0000-0000-0000-000000000001"
}

resource "azurerm_role_assignment" "app_contributor" {
  name                 = "33333333-3333-3333-3333-333333333333"
  principal_id         = "aaaaaaaa-0000-0000-0000-000000000003"
  principal_type       = "ForeignGroup"
  role_definition_id   = "/subscriptions/00000000-0000-0000-0000-000000000001/providers/Microsoft.Authorization/roleDefinitions/b24988ac-6180-42a0-ab88-20f7382dd24c"
  role_definition_name = "Contributor"
  scope                = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app"
}
