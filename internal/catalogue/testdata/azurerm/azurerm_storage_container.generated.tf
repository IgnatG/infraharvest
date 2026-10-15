resource "azurerm_storage_container" "data" {
  container_access_type             = "private"
  default_encryption_scope          = "$account-encryption-key"
  encryption_scope_override_enabled = true
  metadata                          = {}
  name                              = "data"
  storage_account_id                = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.Storage/storageAccounts/stdata"
}

resource "azurerm_storage_container" "audit" {
  container_access_type             = "private"
  default_encryption_scope          = "audit"
  encryption_scope_override_enabled = false
  metadata                          = {}
  name                              = "audit"
  storage_account_id                = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.Storage/storageAccounts/stdata"
}
