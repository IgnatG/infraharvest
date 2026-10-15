// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"maps"
	"slices"
)

// serviceAliases are the names of the services infraharvest listed Google
// Cloud with, one API at a time, before Cloud Asset Inventory took their
// place, and the Terraform types each listed that the catalogue maps from
// Cloud Asset Inventory. Each name still works: it runs cloud_asset for
// those types alone, so --services=networks,firewall lists what it did.
// They are opt-in, as cloud_asset lists them all under *.
//
// Types the old services listed that Cloud Asset Inventory doesn't find
// are gone from them: gcs's bucket IAM policies, bindings and members and
// its notifications, and monitoring's groups. The old iam, dns and
// cloudsql services listed types that Cloud Asset Inventory finds as well
// as some it doesn't; those names now list only the latter (see
// IamLister, DNSRecordSetsLister and CloudSQLLister), with
// cloud_asset for the rest.
var serviceAliases = map[string][]string{
	"addresses":                   {"google_compute_address"},
	"autoscalers":                 {"google_compute_autoscaler"},
	"backendBuckets":              {"google_compute_backend_bucket"},
	"backendServices":             {"google_compute_backend_service"},
	"bigQuery":                    {"google_bigquery_dataset", "google_bigquery_table"},
	"cloudFunctions":              {"google_cloudfunctions_function", "google_cloudfunctions2_function"},
	"cloudbuild":                  {"google_cloudbuild_trigger"},
	"cloudtasks":                  {"google_cloud_tasks_queue"},
	"dataProc":                    {"google_dataproc_cluster"},
	"disks":                       {"google_compute_disk"},
	"externalVpnGateways":         {"google_compute_external_vpn_gateway"},
	"firewall":                    {"google_compute_firewall"},
	"forwardingRules":             {"google_compute_forwarding_rule"},
	"gcs":                         {"google_storage_bucket"},
	"gke":                         {"google_container_cluster", "google_container_node_pool"},
	"globalAddresses":             {"google_compute_global_address"},
	"globalForwardingRules":       {"google_compute_global_forwarding_rule"},
	"healthChecks":                {"google_compute_health_check"},
	"httpHealthChecks":            {"google_compute_http_health_check"},
	"httpsHealthChecks":           {"google_compute_https_health_check"},
	"images":                      {"google_compute_image"},
	"instanceGroupManagers":       {"google_compute_instance_group_manager"},
	"instanceGroups":              {"google_compute_instance_group"},
	"instanceTemplates":           {"google_compute_instance_template"},
	"instances":                   {"google_compute_instance"},
	"interconnectAttachments":     {"google_compute_interconnect_attachment"},
	"kms":                         {"google_kms_key_ring", "google_kms_crypto_key"},
	"logging":                     {"google_logging_metric"},
	"memoryStore":                 {"google_redis_instance"},
	"monitoring":                  {"google_monitoring_alert_policy", "google_monitoring_notification_channel", "google_monitoring_uptime_check_config"},
	"networkEndpointGroups":       {"google_compute_network_endpoint_group"},
	"networks":                    {"google_compute_network"},
	"nodeGroups":                  {"google_compute_node_group"},
	"nodeTemplates":               {"google_compute_node_template"},
	"packetMirrorings":            {"google_compute_packet_mirroring"},
	"project":                     {"google_project"},
	"pubsub":                      {"google_pubsub_topic", "google_pubsub_subscription"},
	"regionAutoscalers":           {"google_compute_region_autoscaler"},
	"regionBackendServices":       {"google_compute_region_backend_service"},
	"regionDisks":                 {"google_compute_region_disk"},
	"regionHealthChecks":          {"google_compute_region_health_check"},
	"regionInstanceGroupManagers": {"google_compute_region_instance_group_manager"},
	"regionTargetHttpProxies":     {"google_compute_region_target_http_proxy"},
	"regionTargetHttpsProxies":    {"google_compute_region_target_https_proxy"},
	"regionUrlMaps":               {"google_compute_region_url_map"},
	"reservations":                {"google_compute_reservation"},
	"resourcePolicies":            {"google_compute_resource_policy"},
	"routers":                     {"google_compute_router"},
	"routes":                      {"google_compute_route"},
	"securityPolicies":            {"google_compute_security_policy"},
	"sslPolicies":                 {"google_compute_ssl_policy"},
	"subnetworks":                 {"google_compute_subnetwork"},
	"targetHttpProxies":           {"google_compute_target_http_proxy"},
	"targetHttpsProxies":          {"google_compute_target_https_proxy"},
	"targetInstances":             {"google_compute_target_instance"},
	"targetPools":                 {"google_compute_target_pool"},
	"targetSslProxies":            {"google_compute_target_ssl_proxy"},
	"targetTcpProxies":            {"google_compute_target_tcp_proxy"},
	"targetVpnGateways":           {"google_compute_vpn_gateway"},
	"urlMaps":                     {"google_compute_url_map"},
	"vpnTunnels":                  {"google_compute_vpn_tunnel"},
}

// removedServices are the names of old services that no alias stands for,
// with the reason, which Select gives when --services names one.
var removedServices = map[string]string{
	"sslCertificates": "Cloud Asset Inventory lists Google-managed and self-managed SSL certificates as one asset type, " +
		"which the catalogue can't tell apart, so google_compute_managed_ssl_certificate isn't listed",
	"regionSslCertificates": "Cloud Asset Inventory lists Google-managed and self-managed SSL certificates as one asset type, " +
		"which the catalogue can't tell apart, so google_compute_region_ssl_certificate isn't listed",
	"regionInstanceGroups": "the google provider has no google_compute_region_instance_group resource to import, only a data source; " +
		"regionInstanceGroupManagers lists the managers of regional instance groups",
}

// OptInServices are the services * leaves out: the aliases, whose types
// cloud_asset lists.
func (*Provider) OptInServices() []string {
	return slices.Sorted(maps.Keys(serviceAliases))
}
