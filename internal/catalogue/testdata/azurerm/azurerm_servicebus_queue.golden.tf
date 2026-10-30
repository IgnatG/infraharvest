# azurerm_servicebus_queue, from data/azurerm/azurerm.yaml

import {
  to = azurerm_servicebus_queue.events
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.ServiceBus/namespaces/sb-premium/queues/events"
}

import {
  to = azurerm_servicebus_queue.orders
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.ServiceBus/namespaces/sb-app/queues/orders"
}

# Left out of generated configuration: max_message_size_in_kilobytes=256

# Generated configuration, without the arguments the catalogue omits:
resource "azurerm_servicebus_queue" "orders" {
  name         = "orders"
  namespace_id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.ServiceBus/namespaces/sb-app"
}

resource "azurerm_servicebus_queue" "events" {
  max_message_size_in_kilobytes = 2048
  name                          = "events"
  namespace_id                  = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.ServiceBus/namespaces/sb-premium"
}
