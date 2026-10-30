# azurerm_linux_virtual_machine, from data/azurerm/azurerm.yaml

import {
  to = azurerm_linux_virtual_machine.scaled
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Compute/virtualMachines/scaled"
}

import {
  to = azurerm_linux_virtual_machine.web
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Compute/virtualMachines/web"
}

# Left out where the plan stays the same, as the default: platform_fault_domain=-1

# Generated configuration, without the arguments the catalogue omits:
resource "azurerm_linux_virtual_machine" "web" {
  admin_username        = "azureuser"
  location              = "uksouth"
  name                  = "web"
  network_interface_ids = ["/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Network/networkInterfaces/web-nic"]
  platform_fault_domain = -1
  resource_group_name   = "rg-app"
  size                  = "Standard_B1s"
  os_disk {
    caching              = "ReadWrite"
    storage_account_type = "Standard_LRS"
  }
}

resource "azurerm_linux_virtual_machine" "scaled" {
  admin_username               = "azureuser"
  location                     = "uksouth"
  name                         = "scaled"
  network_interface_ids        = ["/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Network/networkInterfaces/scaled-nic"]
  platform_fault_domain        = 1
  resource_group_name          = "rg-app"
  size                         = "Standard_B1s"
  virtual_machine_scale_set_id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Compute/virtualMachineScaleSets/pool"
  os_disk {
    caching              = "ReadWrite"
    storage_account_type = "Standard_LRS"
  }
}
