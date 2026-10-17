# azurerm_log_analytics_solution, from data/azurerm/azurerm.yaml

import {
  to = azurerm_log_analytics_solution.updates
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-logs/providers/Microsoft.OperationsManagement/solutions/Updates(logs)"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-logs/providers/Microsoft.OperationsManagement/solutions/SecurityCenterFree(logs): left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-logs/providers/Microsoft.OperationsManagement/solutions/SecurityCenterFree(logs)": the solution Microsoft Defender for Cloud (SecurityCenterFree) or AKS Container insights (ContainerInsights) adds to a Log Analytics workspace; select it to import it
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-logs/providers/Microsoft.OperationsManagement/solutions/ContainerInsights(logs): left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-logs/providers/Microsoft.OperationsManagement/solutions/ContainerInsights(logs)": the solution Microsoft Defender for Cloud (SecurityCenterFree) or AKS Container insights (ContainerInsights) adds to a Log Analytics workspace; select it to import it
