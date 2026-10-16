# azurerm_key_vault, from data/azurerm/azurerm.yaml

import {
  to = azurerm_key_vault.kv_app
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.KeyVault/vaults/kv-app"
}

import {
  to = azurerm_key_vault.kv_ops
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.KeyVault/vaults/kv-ops"
}

# Left out of generated configuration: access_policy=[], network_acls when network_acls.bypass=AzureServices and network_acls.default_action=Allow and network_acls.ip_rules=[] and network_acls.virtual_network_subnet_ids=[]

# Left out where the plan stays the same, as the default: public_network_access_enabled=true, soft_delete_retention_days=90

# Generated configuration, without the arguments the catalogue omits:
resource "azurerm_key_vault" "kv_app" {
  enabled_for_deployment          = false
  enabled_for_disk_encryption     = false
  enabled_for_template_deployment = false
  location                        = "uksouth"
  name                            = "kv-app"
  public_network_access_enabled   = true
  purge_protection_enabled        = false
  rbac_authorization_enabled      = true
  resource_group_name             = "rg-app"
  sku_name                        = "standard"
  soft_delete_retention_days      = 90
  tags = {
    project = "app"
  }
  tenant_id = "00000000-0000-0000-0000-000000000002"
}

resource "azurerm_key_vault" "kv_ops" {
  access_policy = [{
    application_id          = ""
    certificate_permissions = []
    key_permissions         = ["Get"]
    object_id               = "00000000-0000-0000-0000-000000000003"
    secret_permissions      = ["Get", "List"]
    storage_permissions     = []
    tenant_id               = "00000000-0000-0000-0000-000000000002"
  }]
  enabled_for_deployment          = false
  enabled_for_disk_encryption     = false
  enabled_for_template_deployment = false
  location                        = "uksouth"
  name                            = "kv-ops"
  public_network_access_enabled   = false
  purge_protection_enabled        = true
  rbac_authorization_enabled      = false
  resource_group_name             = "rg-app"
  sku_name                        = "standard"
  soft_delete_retention_days      = 7
  tenant_id                       = "00000000-0000-0000-0000-000000000002"
  network_acls {
    bypass                     = "AzureServices"
    default_action             = "Deny"
    ip_rules                   = ["203.0.113.0/24"]
    virtual_network_subnet_ids = []
  }
}
