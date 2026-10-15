# azurerm_monitor_action_group, from data/azurerm/azurerm.yaml

import {
  to = azurerm_monitor_action_group.oncall
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Insights/actionGroups/oncall"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/microsoft.insights/actionGroups/Application Insights Smart Detection: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/microsoft.insights/actionGroups/Application Insights Smart Detection": the action group Azure creates for Application Insights smart detection
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/NetworkWatcherRG/providers/Microsoft.Insights/actionGroups/watchers: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/NetworkWatcherRG/providers/Microsoft.Insights/actionGroups/watchers": in NetworkWatcherRG, which Azure creates with a network watcher per region when a virtual network is created
