# azurerm_storage_account, from data/azurerm/azurerm.yaml

import {
  to = azurerm_storage_account.stapp
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Storage/storageAccounts/stapp"
}

import {
  to = azurerm_storage_account.staudit
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Storage/storageAccounts/staudit"
}

# Left out where the plan stays the same, as the default: access_tier=Hot, account_kind=StorageV2, allow_nested_items_to_be_public=false, dns_endpoint_type=Standard, https_traffic_only_enabled=true, large_file_share_enabled=false, local_user_enabled=true, min_tls_version=TLS1_2, public_network_access=Enabled, public_network_access_enabled=true, queue_encryption_key_type=Service, share_properties.retention_policy.days=7, shared_access_key_enabled=true, table_encryption_key_type=Service
