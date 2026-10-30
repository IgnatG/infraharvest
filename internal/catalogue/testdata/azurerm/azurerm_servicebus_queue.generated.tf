resource "azurerm_servicebus_queue" "orders" {
  max_message_size_in_kilobytes = 256
  name                          = "orders"
  namespace_id                  = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.ServiceBus/namespaces/sb-app"
}

resource "azurerm_servicebus_queue" "events" {
  max_message_size_in_kilobytes = 2048
  name                          = "events"
  namespace_id                  = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.ServiceBus/namespaces/sb-premium"
}
