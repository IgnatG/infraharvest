# azurerm_route_table, from data/azurerm/azurerm.yaml

import {
  to = azurerm_route_table.rt_app
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-network/providers/Microsoft.Network/routeTables/rt-app"
}

# Left out of generated configuration: route

# Generated configuration, without the arguments the catalogue omits:
resource "azurerm_route_table" "rt_app" {
  bgp_route_propagation_enabled = true
  location                      = "uksouth"
  name                          = "rt-app"
  resource_group_name           = "rg-network"
  tags = {
    project = "network"
  }
}
