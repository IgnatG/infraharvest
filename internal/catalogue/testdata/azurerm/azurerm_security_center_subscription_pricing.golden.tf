# azurerm_security_center_subscription_pricing, from data/azurerm/azurerm.yaml

import {
  to = azurerm_security_center_subscription_pricing.virtual_machines
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/providers/Microsoft.Security/pricings/VirtualMachines"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/providers/Microsoft.Security/pricings/StorageAccounts: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/providers/Microsoft.Security/pricings/StorageAccounts": a Microsoft Defender for Cloud plan left on the Free tier, as every subscription has each plan; select it to import it
