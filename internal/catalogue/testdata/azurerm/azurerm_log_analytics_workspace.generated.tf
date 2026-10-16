resource "azurerm_log_analytics_workspace" "log_app" {
  allow_resource_only_permissions         = true
  cmk_for_query_forced                    = false
  daily_quota_gb                          = -1
  data_collection_rule_id                 = null
  immediate_data_purge_on_30_days_enabled = false
  internet_ingestion_access_type          = "Enabled"
  internet_query_access_type              = "Enabled"
  local_authentication_enabled            = true
  location                                = "uksouth"
  name                                    = "log-app"
  reservation_capacity_in_gb_per_day      = null
  resource_group_name                     = "rg-app"
  retention_in_days                       = 30
  sku                                     = "PerGB2018"
  tags = {
    project = "app"
  }
}

resource "azurerm_log_analytics_workspace" "log_audit" {
  allow_resource_only_permissions         = false
  cmk_for_query_forced                    = false
  daily_quota_gb                          = 5
  data_collection_rule_id                 = null
  immediate_data_purge_on_30_days_enabled = false
  internet_ingestion_access_type          = "Enabled"
  internet_query_access_type              = "Disabled"
  local_authentication_enabled            = false
  location                                = "uksouth"
  name                                    = "log-audit"
  reservation_capacity_in_gb_per_day      = null
  resource_group_name                     = "rg-app"
  retention_in_days                       = 90
  sku                                     = "PerGB2018"
}
