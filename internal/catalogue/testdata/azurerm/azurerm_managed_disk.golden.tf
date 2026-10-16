# azurerm_managed_disk, from data/azurerm/azurerm.yaml

import {
  to = azurerm_managed_disk.spare
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Compute/disks/spare"
}

import {
  to = azurerm_managed_disk.vm1_data
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Compute/disks/vm1-data"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Compute/disks/vm1_OsDisk_1: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Compute/disks/vm1_OsDisk_1": the OS disk of a virtual machine, which the machine's os_disk block creates and manages; a data disk attached to a machine is imported
