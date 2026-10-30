# azurerm_postgresql_flexible_server, from data/azurerm/azurerm.yaml

import {
  to = azurerm_postgresql_flexible_server.pg_main
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg-main"
}

import {
  to = azurerm_postgresql_flexible_server.pg_v2
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg-v2"
}

# Left out of generated configuration: storage_iops when !storage_type=PremiumV2_LRS, storage_throughput when !storage_type=PremiumV2_LRS

# Generated configuration, without the arguments the catalogue omits:
resource "azurerm_postgresql_flexible_server" "pg_main" {
  location            = "uksouth"
  name                = "pg-main"
  resource_group_name = "rg-data"
  sku_name            = "B_Standard_B1ms"
  storage_mb          = 32768
  storage_tier        = "P4"
  storage_type        = "Premium_LRS"
  version             = "16"
}

resource "azurerm_postgresql_flexible_server" "pg_v2" {
  location            = "uksouth"
  name                = "pg-v2"
  resource_group_name = "rg-data"
  sku_name            = "GP_Standard_D2s_v3"
  storage_iops        = 3000
  storage_mb          = 65536
  storage_throughput  = 125
  storage_type        = "PremiumV2_LRS"
  version             = "17"
}
