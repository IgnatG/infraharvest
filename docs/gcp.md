### Use with GCP

In order to access the information from your Google Project, you need to provide authentication credentials
by setting up the environment variable `GOOGLE_APPLICATION_CREDENTIALS` with the file path of the JSON
file that contains your service account key. 

Example:

```
infraharvest import google --all --resources=gcs,forwardingRules,httpHealthChecks --regions=europe-west1,europe-west4 --projects=aaa,fff
infraharvest import google --all --resources=gcs,forwardingRules,httpHealthChecks --filter=compute_firewall=rule1:rule2:rule3 --regions=europe-west1 --projects=aaa,fff
```

For google-beta provider:

```
infraharvest import google --all --resources=gcs,forwardingRules,httpHealthChecks --regions=europe-west4 --projects=aaa --provider-type beta
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover google` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

Instead of naming projects, `--organization=<organization ID>` imports every active project of the organization, and `--folders=<folder ID>,...` every active project of those folders, in their subfolders too; projects in `--projects` are added to them. Listing them needs `resourcemanager.projects.list` and `resourcemanager.folders.list` there, which `roles/browser` and `roles/viewer` grant:

```
infraharvest discover google --resources=gcs,networks --folders=123456789012 --parallel=4
```

Each project gets its own roots (`{output}/google/{project}/{region}/`). Projects are imported one after another unless `--parallel` says how many to import at once; a project that can't be imported is reported and the others go on. Each runs its own Terraform and provider, so memory use grows with `--parallel`.

List of supported GCP services:

*   `addresses`
    * `google_compute_address`
*   `autoscalers`
    * `google_compute_autoscaler`
*   `backendBuckets`
    * `google_compute_backend_bucket`
*   `backendServices`
    * `google_compute_backend_service`
*   `bigQuery`
    * `google_bigquery_dataset`
    * `google_bigquery_table`
*   `cloudFunctions`
    * `google_cloudfunctions_function`
    * `google_cloudfunctions2_function`
*   `cloudbuild`
    * `google_cloudbuild_trigger`
*   `cloudsql`
    * `google_sql_database`
    * `google_sql_database_instance`
*   `dataProc`
    * `google_dataproc_cluster`
*   `disks`
    * `google_compute_disk`
*   `dns`
    * `google_dns_managed_zone`
    * `google_dns_record_set`
*   `externalVpnGateways`
    * `google_compute_external_vpn_gateway`
*   `firewall`
    * `google_compute_firewall`
*   `forwardingRules`
    * `google_compute_forwarding_rule`
*   `gcs`
    * `google_storage_bucket`
    * `google_storage_bucket_acl`
    * `google_storage_bucket_iam_binding`
    * `google_storage_bucket_iam_member`
    * `google_storage_bucket_iam_policy`
    * `google_storage_default_object_acl`
    * `google_storage_notification`
*   `gke`
    * `google_container_cluster`
    * `google_container_node_pool`
*   `globalAddresses`
    * `google_compute_global_address`
*   `globalForwardingRules`
    * `google_compute_global_forwarding_rule`
*   `healthChecks`
    * `google_compute_health_check`
*   `httpHealthChecks`
    * `google_compute_http_health_check`
*   `httpsHealthChecks`
    * `google_compute_https_health_check`
*   `iam`
    * `google_project_iam_custom_role`
    * `google_project_iam_member`
    * `google_service_account`
*   `images`
    * `google_compute_image`
*   `instanceGroupManagers`
    * `google_compute_instance_group_manager`
*   `instanceGroups`
    * `google_compute_instance_group`
*   `instanceTemplates`
    * `google_compute_instance_template`
*   `instances`
    * `google_compute_instance`
*   `interconnectAttachments`
    * `google_compute_interconnect_attachment`
*   `kms`
    * `google_kms_crypto_key`
    * `google_kms_key_ring`
*   `logging`
    * `google_logging_metric`
*   `memoryStore`
    * `google_redis_instance`
*   `monitoring`
    * `google_monitoring_alert_policy`
    * `google_monitoring_group`
    * `google_monitoring_notification_channel`
    * `google_monitoring_uptime_check_config`
*   `networks`
    * `google_compute_network`
*   `nodeGroups`
    * `google_compute_node_group`
*   `nodeTemplates`
    * `google_compute_node_template`
*   `packetMirrorings`
    * `google_compute_packet_mirroring`
*   `project`
    * `google_project`
*   `pubsub`
    * `google_pubsub_subscription`
    * `google_pubsub_topic`
*   `regionAutoscalers`
    * `google_compute_region_autoscaler`
*   `regionBackendServices`
    * `google_compute_region_backend_service`
*   `regionDisks`
    * `google_compute_region_disk`
*   `regionHealthChecks`
    * `google_compute_region_health_check`
*   `regionInstanceGroupManagers`
    * `google_compute_region_instance_group_manager`
*   `regionInstanceGroups`
    * `google_compute_region_instance_group`
*   `regionSslCertificates`
    * `google_compute_region_ssl_certificate`
*   `regionTargetHttpProxies`
    * `google_compute_region_target_http_proxy`
*   `regionTargetHttpsProxies`
    * `google_compute_region_target_https_proxy`
*   `regionUrlMaps`
    * `google_compute_region_url_map`
*   `reservations`
    * `google_compute_reservation`
*   `resourcePolicies`
    * `google_compute_resource_policy`
*   `routers`
    * `google_compute_router`
*   `routes`
    * `google_compute_route`
*   `schedulerJobs`
    * `google_cloud_scheduler_job`
*   `securityPolicies`
    * `google_compute_security_policy`
*   `sslCertificates`
    * `google_compute_managed_ssl_certificate`
*   `sslPolicies`
    * `google_compute_ssl_policy`
*   `subnetworks`
    * `google_compute_subnetwork`
*   `targetHttpProxies`
    * `google_compute_target_http_proxy`
*   `targetHttpsProxies`
    * `google_compute_target_https_proxy`
*   `targetInstances`
    * `google_compute_target_instance`
*   `targetPools`
    * `google_compute_target_pool`
*   `targetSslProxies`
    * `google_compute_target_ssl_proxy`
*   `targetTcpProxies`
    * `google_compute_target_tcp_proxy`
*   `targetVpnGateways`
    * `google_compute_vpn_gateway`
*   `urlMaps`
    * `google_compute_url_map`
*   `vpnTunnels`
    * `google_compute_vpn_tunnel`
