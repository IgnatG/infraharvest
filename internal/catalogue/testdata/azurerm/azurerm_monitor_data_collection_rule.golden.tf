# azurerm_monitor_data_collection_rule, from data/azurerm/azurerm.yaml

import {
  to = azurerm_monitor_data_collection_rule.syslog
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-aks/providers/Microsoft.Insights/dataCollectionRules/syslog"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-aks/providers/Microsoft.Insights/dataCollectionRules/MSCI-eastus-aks: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-aks/providers/Microsoft.Insights/dataCollectionRules/MSCI-eastus-aks": the data collection rule AKS creates when Container insights (MSCI-<region>-<cluster>) or managed Prometheus (MSProm-<region>-<cluster>) is enabled on a cluster; select it to import it
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-aks/providers/Microsoft.Insights/dataCollectionRules/MSProm-eastus-aks: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-aks/providers/Microsoft.Insights/dataCollectionRules/MSProm-eastus-aks": the data collection rule AKS creates when Container insights (MSCI-<region>-<cluster>) or managed Prometheus (MSProm-<region>-<cluster>) is enabled on a cluster; select it to import it
