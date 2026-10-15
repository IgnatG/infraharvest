# azurerm_storage_account_queue_properties, from data/azurerm/azurerm.yaml

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.Storage/storageAccounts/stdata: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.Storage/storageAccounts/stdata": the queue service settings every storage account has, which Terraform reads through the storage data plane, with the account's keys or a data role, which the Reader role doesn't grant; select it to import it
