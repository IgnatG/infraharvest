# azurerm_network_interface, from data/azurerm/azurerm.yaml

import {
  to = azurerm_network_interface.vm1_nic
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Network/networkInterfaces/vm1-nic"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Network/networkInterfaces/pe-blob.nic.3f2a9c1e-0000-0000-0000-000000000001: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Network/networkInterfaces/pe-blob.nic.3f2a9c1e-0000-0000-0000-000000000001": the network interface Azure creates for a private endpoint, which the endpoint creates and deletes with itself
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Network/networkInterfaces/pls-app.nic.3f2a9c1e-0000-0000-0000-000000000002: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Network/networkInterfaces/pls-app.nic.3f2a9c1e-0000-0000-0000-000000000002": the network interface Azure creates for a Private Link service, which the service creates and deletes with itself
