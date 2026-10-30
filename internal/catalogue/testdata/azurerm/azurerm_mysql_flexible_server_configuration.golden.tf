# azurerm_mysql_flexible_server_configuration, from data/azurerm/azurerm.yaml

import {
  to = azurerm_mysql_flexible_server_configuration.slow_query_log
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.DBforMySQL/flexibleServers/my-main/configurations/slow_query_log"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.DBforMySQL/flexibleServers/my-main/configurations/sql_mode: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-data/providers/Microsoft.DBforMySQL/flexibleServers/my-main/configurations/sql_mode": a server parameter at the service's default (source system-default); only a parameter someone set is imported
