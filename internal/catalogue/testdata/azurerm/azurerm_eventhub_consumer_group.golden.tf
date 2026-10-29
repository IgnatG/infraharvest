# azurerm_eventhub_consumer_group, from data/azurerm/azurerm.yaml

import {
  to = azurerm_eventhub_consumer_group.analytics
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-msg/providers/Microsoft.EventHub/namespaces/eh-events/eventhubs/events/consumerGroups/analytics"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-msg/providers/Microsoft.EventHub/namespaces/eh-events/eventhubs/events/consumerGroups/$Default: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-msg/providers/Microsoft.EventHub/namespaces/eh-events/eventhubs/events/consumerGroups/$Default": the $Default consumer group Azure creates with every event hub, which can't be deleted
