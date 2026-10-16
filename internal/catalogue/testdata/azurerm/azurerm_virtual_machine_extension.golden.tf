# azurerm_virtual_machine_extension, from data/azurerm/azurerm.yaml

import {
  to = azurerm_virtual_machine_extension.custom_script
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Compute/virtualMachines/vm1/extensions/CustomScript"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Compute/virtualMachines/vm1/extensions/MDE.Linux: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Compute/virtualMachines/vm1/extensions/MDE.Linux": an extension Microsoft Defender for Cloud installs on the virtual machine (Defender for Endpoint, MDE.*, or its security agent); select it to import it
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Compute/virtualMachines/vm1/extensions/AzureSecurityLinuxAgent: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Compute/virtualMachines/vm1/extensions/AzureSecurityLinuxAgent": an extension Microsoft Defender for Cloud installs on the virtual machine (Defender for Endpoint, MDE.*, or its security agent); select it to import it
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Compute/virtualMachines/vm1/extensions/AzurePolicyforLinux: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Compute/virtualMachines/vm1/extensions/AzurePolicyforLinux": the guest configuration extension Azure Policy installs on the virtual machine (AzurePolicyforLinux, AzurePolicyforWindows); select it to import it
