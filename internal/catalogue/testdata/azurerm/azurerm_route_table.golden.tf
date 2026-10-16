# azurerm_route_table, from data/azurerm/azurerm.yaml

import {
  to = azurerm_route_table.rt_app
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-network/providers/Microsoft.Network/routeTables/rt-app"
}

import {
  to = azurerm_route_table.rt_firewall
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-network/providers/Microsoft.Network/routeTables/rt-firewall"
}

# Left out of generated configuration: route

# Left out where the plan stays the same, as the default: bgp_route_propagation_enabled=true

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

resource "azurerm_route_table" "rt_firewall" {
  bgp_route_propagation_enabled = false
  location                      = "uksouth"
  name                          = "rt-firewall"
  resource_group_name           = "rg-network"
}
