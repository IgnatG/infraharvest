# azurerm_network_watcher, from data/azurerm/azurerm.yaml

import {
  to = azurerm_network_watcher.watcher
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-net/providers/Microsoft.Network/networkWatchers/watcher"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/NetworkWatcherRG/providers/Microsoft.Network/networkWatchers/NetworkWatcher_eastus: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/NetworkWatcherRG/providers/Microsoft.Network/networkWatchers/NetworkWatcher_eastus": a network watcher in NetworkWatcherRG, which Azure creates for the region when a virtual network is created; its flow logs and connection monitors are imported
