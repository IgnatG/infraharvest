# azurerm_log_analytics_workspace, from data/azurerm/azurerm.yaml

import {
  to = azurerm_log_analytics_workspace.log_app
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.OperationalInsights/workspaces/log-app"
}

import {
  to = azurerm_log_analytics_workspace.log_audit
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.OperationalInsights/workspaces/log-audit"
}

# Left out where the plan stays the same, as the default: allow_resource_only_permissions=true, daily_quota_gb=-1, internet_ingestion_access_type=Enabled, internet_query_access_type=Enabled, local_authentication_enabled=true, retention_in_days=30, sku=PerGB2018
