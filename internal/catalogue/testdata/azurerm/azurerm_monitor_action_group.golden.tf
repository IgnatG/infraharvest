# azurerm_monitor_action_group, from data/azurerm/azurerm.yaml

import {
  to = azurerm_monitor_action_group.oncall
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/Microsoft.Insights/actionGroups/oncall"
}

import {
  to = azurerm_monitor_action_group.watchers
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/NetworkWatcherRG/providers/Microsoft.Insights/actionGroups/watchers"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/microsoft.insights/actionGroups/Application Insights Smart Detection: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-app/providers/microsoft.insights/actionGroups/Application Insights Smart Detection": the action group Azure creates for Application Insights smart detection
