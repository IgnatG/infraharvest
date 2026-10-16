# azurerm_monitor_alert_prometheus_rule_group, from data/azurerm/azurerm.yaml

import {
  to = azurerm_monitor_alert_prometheus_rule_group.pod_alerts
  id = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-aks/providers/Microsoft.AlertsManagement/prometheusRuleGroups/pod-alerts"
}

# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-aks/providers/Microsoft.AlertsManagement/prometheusRuleGroups/NodeRecordingRulesRuleGroup-aks: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-aks/providers/Microsoft.AlertsManagement/prometheusRuleGroups/NodeRecordingRulesRuleGroup-aks": a recording rule group AKS creates when managed Prometheus is enabled on a cluster, such as NodeRecordingRulesRuleGroup-<cluster>; select it to import it
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-aks/providers/Microsoft.AlertsManagement/prometheusRuleGroups/KubernetesRecordingRulesRuleGroup-aks: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-aks/providers/Microsoft.AlertsManagement/prometheusRuleGroups/KubernetesRecordingRulesRuleGroup-aks": a recording rule group AKS creates when managed Prometheus is enabled on a cluster, such as NodeRecordingRulesRuleGroup-<cluster>; select it to import it
# /subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-aks/providers/Microsoft.AlertsManagement/prometheusRuleGroups/NodeAndKubernetesRecordingRulesRuleGroup-Win-aks: left out by default, would import as "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/rg-aks/providers/Microsoft.AlertsManagement/prometheusRuleGroups/NodeAndKubernetesRecordingRulesRuleGroup-Win-aks": a recording rule group AKS creates when managed Prometheus is enabled on a cluster, such as NodeRecordingRulesRuleGroup-<cluster>; select it to import it
