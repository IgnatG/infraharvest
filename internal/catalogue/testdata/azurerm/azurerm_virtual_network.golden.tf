# azurerm_virtual_network, from data/azurerm/azurerm.yaml

import {
  to = azurerm_virtual_network.vnet_hub
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-network/providers/Microsoft.Network/virtualNetworks/vnet-hub"
}

import {
  to = azurerm_virtual_network.vnet_spoke
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-network/providers/Microsoft.Network/virtualNetworks/vnet-spoke"
}

# Left out of generated configuration: subnet, dns_servers=[]

# Left out where the plan stays the same, as the default: private_endpoint_vnet_policies=Disabled

# Generated configuration, without the arguments the catalogue omits:
resource "azurerm_virtual_network" "vnet_hub" {
  address_space                  = ["10.70.0.0/16"]
  bgp_community                  = null
  edge_zone                      = null
  location                       = "uksouth"
  name                           = "vnet-hub"
  private_endpoint_vnet_policies = "Disabled"
  resource_group_name            = "rg-network"
  tags = {
    project = "network"
  }
}

resource "azurerm_virtual_network" "vnet_spoke" {
  address_space                  = ["10.71.0.0/16"]
  bgp_community                  = null
  dns_servers                    = ["10.70.0.4"]
  edge_zone                      = null
  location                       = "uksouth"
  name                           = "vnet-spoke"
  private_endpoint_vnet_policies = "Basic"
  resource_group_name            = "rg-network"
}
