# azurerm_monitor_diagnostic_setting, from data/azurerm/azurerm.yaml

import {
  to = azurerm_monitor_diagnostic_setting.data_blobServices_blob_logs
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app/providers/Microsoft.Storage/storageAccounts/data/blobServices/default|blob-logs"
}

import {
  to = azurerm_monitor_diagnostic_setting.secrets_audit
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app/providers/Microsoft.KeyVault/vaults/secrets|audit"
}

import {
  to = azurerm_monitor_diagnostic_setting.subscription_activity
  id = "/subscriptions/00000000-0000-0000-0000-000000000001|activity"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app/providers/Microsoft.KeyVault/vaults/secrets/providers/Microsoft.Insights/diagnosticSettings/setByPolicy-LogAnalytics: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app/providers/Microsoft.KeyVault/vaults/secrets|setByPolicy-LogAnalytics": a diagnostic setting an Azure Policy DeployIfNotExists assignment deploys and keeps as the policy has it (setByPolicy...); select it to import it
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/MC_app_aks_westeurope/providers/Microsoft.Network/loadBalancers/kubernetes/providers/Microsoft.Insights/diagnosticSettings/lb: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/MC_app_aks_westeurope/providers/Microsoft.Network/loadBalancers/kubernetes|lb": in a resource group another resource manages, such as an AKS cluster's node resource group (MC_*); that resource creates and deletes what is in it
