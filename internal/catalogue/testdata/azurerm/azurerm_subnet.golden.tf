# azurerm_subnet, from data/azurerm/azurerm.yaml

import {
  to = azurerm_subnet.app
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-network/providers/Microsoft.Network/virtualNetworks/vnet-hub/subnets/app"
}

import {
  to = azurerm_subnet.private
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-network/providers/Microsoft.Network/virtualNetworks/vnet-hub/subnets/private"
}

# Left out where the plan stays the same, as the default: default_outbound_access_enabled=true, private_endpoint_network_policies=Disabled, private_link_service_network_policies_enabled=true
