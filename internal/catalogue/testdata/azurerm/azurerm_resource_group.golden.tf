# azurerm_resource_group, from data/azurerm/azurerm.yaml

import {
  to = azurerm_resource_group.rg_app
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/NetworkWatcherRG: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/NetworkWatcherRG": NetworkWatcherRG, which Azure creates to hold a network watcher per region when a virtual network is created
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/MC_rg-aks_aks_eastus: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/MC_rg-aks_aks_eastus": another resource manages it (managedBy), such as the node resource group (MC_*) an AKS cluster creates and deletes, or a managed application's resource group
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/AzureBackupRG_eastus_1: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/AzureBackupRG_eastus_1": in an AzureBackupRG_<region>_<n> resource group, which Azure Backup creates for the restore point collections of the virtual machines it backs up; select it to import it
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/DefaultResourceGroup-EUS: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/DefaultResourceGroup-EUS": in a DefaultResourceGroup-<region>, which Azure creates for default Log Analytics workspaces (Container insights, Defender for Cloud)
