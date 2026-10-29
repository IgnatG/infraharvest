# azurerm_eventhub_namespace_authorization_rule, from data/azurerm/azurerm.yaml

import {
  to = azurerm_eventhub_namespace_authorization_rule.listen
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-msg/providers/Microsoft.EventHub/namespaces/eh-events/authorizationRules/listen"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-msg/providers/Microsoft.EventHub/namespaces/eh-events/authorizationRules/RootManageSharedAccessKey: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-msg/providers/Microsoft.EventHub/namespaces/eh-events/authorizationRules/RootManageSharedAccessKey": the RootManageSharedAccessKey rule Azure creates with every Event Hubs namespace, whose keys the namespace's default connection strings are; select it to import it
