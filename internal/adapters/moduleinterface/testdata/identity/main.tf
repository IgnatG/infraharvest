# A module that manages azurerm resources, with a local module (whose
# resources count) and a registry module (whose resources don't).

resource "azurerm_user_assigned_identity" "this" {
  location            = var.location
  name                = var.name
  resource_group_name = var.resource_group_name
}

resource "azurerm_federated_identity_credential" "this" {
  for_each = var.federated_identity_credentials

  audience                  = each.value.audience
  issuer                    = each.value.issuer
  name                      = each.value.name
  subject                   = each.value.subject
  user_assigned_identity_id = azurerm_user_assigned_identity.this.id
}

module "locks" {
  source = "./modules/locks"

  scope = azurerm_user_assigned_identity.this.id
}

module "naming" {
  source  = "Azure/naming/azurerm"
  version = "0.4.2"
}

data "azurerm_client_config" "current" {}
