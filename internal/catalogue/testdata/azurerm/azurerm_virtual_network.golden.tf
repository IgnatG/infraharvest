# azurerm_virtual_network, from data/azurerm/azurerm.yaml

import {
  to = azurerm_virtual_network.vnet_hub
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-network/providers/Microsoft.Network/virtualNetworks/vnet-hub"
}

# Left out of generated configuration: subnet

# Generated configuration, without the arguments the catalogue omits:
resource "azurerm_virtual_network" "vnet_hub" {
  address_space                  = ["10.70.0.0/16"]
  bgp_community                  = null
  dns_servers                    = []
  edge_zone                      = null
  location                       = "uksouth"
  name                           = "vnet-hub"
  private_endpoint_vnet_policies = "Disabled"
  resource_group_name            = "rg-network"
  tags = {
    project = "network"
  }
}
