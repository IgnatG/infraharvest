# azurerm_management_lock, from data/azurerm/azurerm.yaml

import {
  to = azurerm_management_lock.keep
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app/providers/Microsoft.Authorization/locks/keep"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app/providers/Microsoft.Storage/storageAccounts/data/providers/Microsoft.Authorization/locks/AzureBackupProtectionLock: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app/providers/Microsoft.Storage/storageAccounts/data/providers/Microsoft.Authorization/locks/AzureBackupProtectionLock": the delete lock Azure Backup adds to a storage account whose file shares it protects, and expects to stay; select it to import it
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/DefaultResourceGroup-WEU/providers/Microsoft.Authorization/locks/workspace: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/DefaultResourceGroup-WEU/providers/Microsoft.Authorization/locks/workspace": in a DefaultResourceGroup-<region>, which Azure creates for default Log Analytics workspaces (Container insights, Defender for Cloud)
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/MC_rg-aks_aks_eastus/providers/Microsoft.Network/loadBalancers/kubernetes/providers/Microsoft.Authorization/locks/nodes: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/MC_rg-aks_aks_eastus/providers/Microsoft.Network/loadBalancers/kubernetes/providers/Microsoft.Authorization/locks/nodes": in a resource group another resource manages, such as an AKS cluster's node resource group (MC_*); that resource creates and deletes what is in it
