provider "azurerm" {
  features {}
  subscription_id = var.subscription_id
}

# Reader can see every resource's configuration but has no data actions:
# no blob contents, Key Vault secrets or keys, or database rows.
resource "azurerm_role_assignment" "reader" {
  scope                = coalesce(var.scope, "/subscriptions/${var.subscription_id}")
  role_definition_name = "Reader"
  principal_id         = var.principal_id
  description          = "infraharvest: read-only access to resource configuration."
}
