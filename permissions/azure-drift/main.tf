provider "azurerm" {
  features {}
  subscription_id = var.subscription_id
}

# The Reader role permissions/azure assigns, which refreshing the roots
# needs: left out when the principal has it from there already.
resource "azurerm_role_assignment" "reader" {
  count = var.assign_reader ? 1 : 0

  scope                = coalesce(var.scope, "/subscriptions/${var.subscription_id}")
  role_definition_name = "Reader"
  principal_id         = var.principal_id
  description          = "infraharvest: read-only access to resource configuration."
}

# Reads the roots' state blobs with the identity's own token
# (ARM_USE_AZUREAD=true), on the state's container alone: no access key,
# which would open the whole storage account. Drift plans with
# -lock=false, so it never takes the blob's lease.
resource "azurerm_role_assignment" "state" {
  scope                = "${var.state_storage_account_id}/blobServices/default/containers/${var.state_container_name}"
  role_definition_name = "Storage Blob Data Reader"
  principal_id         = var.principal_id
  description          = "infraharvest drift: read access to the roots' state."
}
