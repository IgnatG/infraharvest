# azurerm_network_security_group, from data/azurerm/azurerm.yaml

import {
  to = azurerm_network_security_group.nsg_app
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-network/providers/Microsoft.Network/networkSecurityGroups/nsg-app"
}

# Left out of generated configuration: security_rule

# Generated configuration, without the arguments the catalogue omits:
resource "azurerm_network_security_group" "nsg_app" {
  location            = "uksouth"
  name                = "nsg-app"
  resource_group_name = "rg-network"
  tags = {
    project = "network"
  }
}
