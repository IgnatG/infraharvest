# azurerm_postgresql_flexible_server_configuration, from data/azurerm/azurerm.yaml

import {
  to = azurerm_postgresql_flexible_server_configuration.azure_extensions
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg-main/configurations/azure.extensions"
}

import {
  to = azurerm_postgresql_flexible_server_configuration.work_mem
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg-main/configurations/work_mem"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg-main/configurations/application_name: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg-main/configurations/application_name": a server parameter at the service's default (source system-default); only a parameter someone set is imported
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg-main/configurations/anon.salt: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg-main/configurations/anon.salt": a server parameter at the service's default (source system-default); only a parameter someone set is imported
