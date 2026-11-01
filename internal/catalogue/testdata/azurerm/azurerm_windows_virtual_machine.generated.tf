resource "azurerm_windows_virtual_machine" "win" {
  admin_password        = null # sensitive
  admin_username        = "azureuser"
  location              = "uksouth"
  name                  = "win"
  network_interface_ids = ["/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Network/networkInterfaces/win-nic"]
  platform_fault_domain = -1
  resource_group_name   = "rg-app"
  size                  = "Standard_B2s"
  os_disk {
    caching              = "ReadWrite"
    storage_account_type = "Standard_LRS"
  }
}
