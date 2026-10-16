# azurerm_monitor_data_collection_endpoint, from data/azurerm/azurerm.yaml

import {
  to = azurerm_monitor_data_collection_endpoint.logs
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-aks/providers/Microsoft.Insights/dataCollectionEndpoints/logs"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-aks/providers/Microsoft.Insights/dataCollectionEndpoints/MSProm-eastus-aks: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-aks/providers/Microsoft.Insights/dataCollectionEndpoints/MSProm-eastus-aks": the data collection endpoint AKS creates when managed Prometheus is enabled on a cluster (MSProm-<region>-<cluster>); select it to import it
