# azurerm_servicebus_namespace_authorization_rule, from data/azurerm/azurerm.yaml

import {
  to = azurerm_servicebus_namespace_authorization_rule.send
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-msg/providers/Microsoft.ServiceBus/namespaces/sb-orders/authorizationRules/send"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-msg/providers/Microsoft.ServiceBus/namespaces/sb-orders/authorizationRules/RootManageSharedAccessKey: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-msg/providers/Microsoft.ServiceBus/namespaces/sb-orders/authorizationRules/RootManageSharedAccessKey": the RootManageSharedAccessKey rule Azure creates with every Service Bus namespace, whose keys the namespace's default connection strings are; select it to import it
