# azurerm_log_analytics_workspace_table_microsoft, from data/azurerm/azurerm.yaml

import {
  to = azurerm_log_analytics_workspace_table_microsoft.apptraces
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-logs/providers/Microsoft.OperationalInsights/workspaces/logs/tables/AppTraces"
}

import {
  to = azurerm_log_analytics_workspace_table_microsoft.containerlogv2
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-logs/providers/Microsoft.OperationalInsights/workspaces/logs/tables/ContainerLogV2"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-logs/providers/Microsoft.OperationalInsights/workspaces/logs/tables/AACAudit: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-logs/providers/Microsoft.OperationalInsights/workspaces/logs/tables/AACAudit": a table Azure builds into every Log Analytics workspace, with its default plan and retention; select it to import it
