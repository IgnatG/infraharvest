resource "azurerm_storage_account" "stapp" {
  access_tier                       = "Hot"
  account_kind                      = "StorageV2"
  account_replication_type          = "LRS"
  account_tier                      = "Standard"
  allow_nested_items_to_be_public   = false
  allowed_copy_scope                = null
  cross_tenant_replication_enabled  = false
  default_to_oauth_authentication   = false
  dns_endpoint_type                 = "Standard"
  edge_zone                         = null
  https_traffic_only_enabled        = true
  infrastructure_encryption_enabled = false
  is_hns_enabled                    = false
  large_file_share_enabled          = false
  local_user_enabled                = true
  location                          = "uksouth"
  min_tls_version                   = "TLS1_2"
  name                              = "stapp"
  nfsv3_enabled                     = false
  provisioned_billing_model_version = null
  public_network_access             = "Enabled"
  public_network_access_enabled     = true
  queue_encryption_key_type         = "Service"
  resource_group_name               = "rg-app"
  sftp_enabled                      = false
  shared_access_key_enabled         = true
  table_encryption_key_type         = "Service"
  tags = {
    project = "app"
  }
  blob_properties {
    change_feed_enabled           = false
    change_feed_retention_in_days = 0
    default_service_version       = ""
    last_access_time_enabled      = false
    versioning_enabled            = false
  }
  share_properties {
    retention_policy {
      days = 7
    }
  }
}

resource "azurerm_storage_account" "staudit" {
  access_tier                       = "Cool"
  account_kind                      = "StorageV2"
  account_replication_type          = "ZRS"
  account_tier                      = "Standard"
  allow_nested_items_to_be_public   = true
  allowed_copy_scope                = "AAD"
  cross_tenant_replication_enabled  = false
  default_to_oauth_authentication   = true
  dns_endpoint_type                 = "Standard"
  edge_zone                         = null
  https_traffic_only_enabled        = true
  infrastructure_encryption_enabled = false
  is_hns_enabled                    = false
  large_file_share_enabled          = true
  local_user_enabled                = false
  location                          = "uksouth"
  min_tls_version                   = "TLS1_2"
  name                              = "staudit"
  nfsv3_enabled                     = false
  provisioned_billing_model_version = null
  public_network_access             = "Disabled"
  public_network_access_enabled     = false
  queue_encryption_key_type         = "Account"
  resource_group_name               = "rg-app"
  sftp_enabled                      = false
  shared_access_key_enabled         = false
  table_encryption_key_type         = "Account"
  share_properties {
    retention_policy {
      days = 30
    }
  }
}
