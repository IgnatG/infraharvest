# Services

<!-- Generated from the providers by TestGoldenServicesReference in internal/cli/servicesreference_test.go: don't edit. -->

The services each provider lists, as `--services` and `--skip` name them, and the Terraform resource types each one lists. The types are those named in the service's lister code, those the [catalogue](../catalogue.md) assigns to the service, and the child resources the catalogue imports with them. A service lists only the types an account has, and a few services decide the type of a resource from what the cloud returns, so the cloud can add types not shown here.

`--services=*` lists every service except the opt-in ones, which list through the cloud's inventory API what other services already list: name them to use them. `infraharvest services <provider>` prints a provider's services.

## AWS

`infraharvest import aws` has 99 services.

| Service | Terraform types |
|---|---|
| `accessanalyzer` | `aws_accessanalyzer_analyzer` |
| `acm` | `aws_acm_certificate` |
| `alb` | `aws_lb`, `aws_lb_listener`, `aws_lb_listener_certificate`, `aws_lb_listener_rule`, `aws_lb_target_group`, `aws_lb_target_group_attachment` |
| `api_gateway` | `aws_api_gateway_api_key`, `aws_api_gateway_authorizer`, `aws_api_gateway_deployment`, `aws_api_gateway_documentation_part`, `aws_api_gateway_gateway_response`, `aws_api_gateway_integration`, `aws_api_gateway_integration_response`, `aws_api_gateway_method`, `aws_api_gateway_method_response`, `aws_api_gateway_model`, `aws_api_gateway_resource`, `aws_api_gateway_rest_api`, `aws_api_gateway_stage`, `aws_api_gateway_usage_plan`, `aws_api_gateway_vpc_link` |
| `api_gatewayv2` | `aws_apigatewayv2_api`, `aws_apigatewayv2_authorizer`, `aws_apigatewayv2_integration`, `aws_apigatewayv2_model`, `aws_apigatewayv2_route`, `aws_apigatewayv2_route_response`, `aws_apigatewayv2_stage`, `aws_apigatewayv2_vpc_link` |
| `appconfig` | `aws_appconfig_application` |
| `appsync` | `aws_appsync_graphql_api` |
| `athena` | `aws_athena_workgroup` |
| `auto_scaling` | `aws_autoscaling_group`, `aws_launch_configuration`, `aws_launch_template` |
| `backup` | `aws_backup_plan`, `aws_backup_vault` |
| `batch` | `aws_batch_compute_environment`, `aws_batch_job_definition`, `aws_batch_job_queue` |
| `budgets` | `aws_budgets_budget` |
| `cloud9` | `aws_cloud9_environment_ec2` |
| `cloudformation` | `aws_cloudformation_stack`, `aws_cloudformation_stack_set`, `aws_cloudformation_stack_set_instance` |
| `cloudfront` | `aws_cloudfront_cache_policy`, `aws_cloudfront_distribution` |
| `cloudhsm` | `aws_cloudhsm_v2_cluster`, `aws_cloudhsm_v2_hsm` |
| `cloudtrail` | `aws_cloudtrail` |
| `cloudwatch` | `aws_cloudwatch_dashboard`, `aws_cloudwatch_event_rule`, `aws_cloudwatch_event_target`, `aws_cloudwatch_metric_alarm` |
| `codeartifact` | `aws_codeartifact_domain`, `aws_codeartifact_repository` |
| `codebuild` | `aws_codebuild_project` |
| `codecommit` | `aws_codecommit_approval_rule_template`, `aws_codecommit_repository` |
| `codedeploy` | `aws_codedeploy_app` |
| `codepipeline` | `aws_codepipeline`, `aws_codepipeline_webhook` |
| `cognito` | `aws_cognito_identity_pool`, `aws_cognito_user_pool`, `aws_cognito_user_pool_client` |
| `config` | `aws_config_config_rule`, `aws_config_configuration_recorder`, `aws_config_delivery_channel` |
| `customer_gateway` | `aws_customer_gateway` |
| `datapipeline` | `aws_datapipeline_pipeline` |
| `devicefarm` | `aws_devicefarm_project` |
| `docdb` | `aws_docdb_cluster`, `aws_docdb_cluster_instance`, `aws_docdb_cluster_parameter_group`, `aws_docdb_subnet_group` |
| `dx` | `aws_dx_connection`, `aws_dx_gateway`, `aws_dx_private_virtual_interface`, `aws_dx_public_virtual_interface`, `aws_dx_transit_virtual_interface` |
| `dynamodb` | `aws_dynamodb_table` |
| `ebs` | `aws_ebs_volume`, `aws_volume_attachment` |
| `ec2_instance` | `aws_instance` |
| `ecr` | `aws_ecr_lifecycle_policy`, `aws_ecr_repository`, `aws_ecr_repository_policy` |
| `ecrpublic` | `aws_ecrpublic_repository` |
| `ecs` | `aws_ecs_cluster`, `aws_ecs_service`, `aws_ecs_task_definition` |
| `efs` | `aws_efs_access_point`, `aws_efs_file_system`, `aws_efs_file_system_policy`, `aws_efs_mount_target` |
| `eip` | `aws_eip` |
| `eks` | `aws_eks_access_entry`, `aws_eks_access_policy_association`, `aws_eks_addon`, `aws_eks_cluster`, `aws_eks_fargate_profile`, `aws_eks_node_group`, `aws_eks_pod_identity_association` |
| `elastic_beanstalk` | `aws_elastic_beanstalk_application`, `aws_elastic_beanstalk_environment` |
| `elasticache` | `aws_elasticache_cluster`, `aws_elasticache_parameter_group`, `aws_elasticache_replication_group`, `aws_elasticache_subnet_group` |
| `elb` | `aws_elb` |
| `emr` | `aws_emr_cluster`, `aws_emr_security_configuration` |
| `eni` | `aws_network_interface` |
| `es` | `aws_elasticsearch_domain`, `aws_opensearch_domain` |
| `eventbridge` | `aws_cloudwatch_event_api_destination`, `aws_cloudwatch_event_archive`, `aws_cloudwatch_event_bus`, `aws_cloudwatch_event_connection` |
| `firehose` | `aws_kinesis_firehose_delivery_stream` |
| `glue` | `aws_glue_catalog_database`, `aws_glue_catalog_table`, `aws_glue_crawler`, `aws_glue_job`, `aws_glue_trigger` |
| `guardduty` | `aws_guardduty_detector` |
| `iam` | `aws_iam_access_key`, `aws_iam_group`, `aws_iam_group_policy`, `aws_iam_group_policy_attachment`, `aws_iam_instance_profile`, `aws_iam_policy`, `aws_iam_role`, `aws_iam_role_policy`, `aws_iam_role_policy_attachment`, `aws_iam_user`, `aws_iam_user_group_membership`, `aws_iam_user_policy`, `aws_iam_user_policy_attachment` |
| `identitystore` | `aws_identitystore_group`, `aws_identitystore_group_membership`, `aws_identitystore_user` |
| `igw` | `aws_internet_gateway` |
| `iot` | `aws_iot_role_alias`, `aws_iot_thing`, `aws_iot_thing_type`, `aws_iot_topic_rule` |
| `kinesis` | `aws_kinesis_stream` |
| `kms` | `aws_kms_alias`, `aws_kms_key` |
| `lambda` | `aws_lambda_event_source_mapping`, `aws_lambda_function`, `aws_lambda_function_event_invoke_config`, `aws_lambda_layer_version`, `aws_lambda_permission` |
| `logs` | `aws_cloudwatch_log_group` |
| `media_package` | `aws_media_package_channel` |
| `media_store` | `aws_media_store_container` |
| `medialive` | `aws_medialive_channel`, `aws_medialive_input`, `aws_medialive_input_security_group` |
| `mq` | `aws_mq_broker` |
| `msk` | `aws_msk_cluster` |
| `nacl` | `aws_default_network_acl`, `aws_network_acl` |
| `nat` | `aws_nat_gateway` |
| `organization` | `aws_managed`, `aws_organizations_account`, `aws_organizations_organization`, `aws_organizations_organizational_unit`, `aws_organizations_policy`, `aws_organizations_policy_attachment` |
| `pipes` | `aws_pipes_pipe` |
| `placement_group` | `aws_placement_group` |
| `rds` | `aws_db_cluster_snapshot`, `aws_db_event_subscription`, `aws_db_instance`, `aws_db_option_group`, `aws_db_parameter_group`, `aws_db_proxy`, `aws_db_snapshot`, `aws_db_subnet_group`, `aws_rds_cluster`, `aws_rds_cluster_instance`, `aws_rds_global_cluster` |
| `redshift` | `aws_redshift_cluster`, `aws_redshift_event_subscription`, `aws_redshift_parameter_group`, `aws_redshift_snapshot_schedule`, `aws_redshift_snapshot_schedule_association`, `aws_redshift_subnet_group` |
| `resourcegroups` | `aws_resourcegroups_group` |
| `route53` | `aws_route53_health_check`, `aws_route53_record`, `aws_route53_zone` |
| `route53resolver` | `aws_route53_resolver_endpoint`, `aws_route53_resolver_rule` |
| `route_table` | `aws_main_route_table_association`, `aws_route_table`, `aws_route_table_association` |
| `s3` | `aws_s3_bucket`, `aws_s3_bucket_accelerate_configuration`, `aws_s3_bucket_cors_configuration`, `aws_s3_bucket_lifecycle_configuration`, `aws_s3_bucket_logging`, `aws_s3_bucket_object_lock_configuration`, `aws_s3_bucket_ownership_controls`, `aws_s3_bucket_policy`, `aws_s3_bucket_public_access_block`, `aws_s3_bucket_replication_configuration`, `aws_s3_bucket_request_payment_configuration`, `aws_s3_bucket_server_side_encryption_configuration`, `aws_s3_bucket_versioning`, `aws_s3_bucket_website_configuration` |
| `scheduler` | `aws_scheduler_schedule_group` |
| `secretsmanager` | `aws_secretsmanager_secret` |
| `securityhub` | `aws_securityhub_account`, `aws_securityhub_member`, `aws_securityhub_standards_subscription` |
| `servicecatalog` | `aws_servicecatalog_portfolio` |
| `ses` | `aws_ses_configuration_set`, `aws_ses_domain_identity`, `aws_ses_email_identity`, `aws_ses_receipt_rule`, `aws_ses_receipt_rule_set`, `aws_ses_template` |
| `sfn` | `aws_sfn_activity`, `aws_sfn_state_machine` |
| `sg` | `aws_security_group`, `aws_security_group_rule`, `aws_vpc_security_group_egress_rule`, `aws_vpc_security_group_ingress_rule` |
| `sns` | `aws_sns_topic`, `aws_sns_topic_subscription` |
| `sqs` | `aws_sqs_queue` |
| `ssm` | `aws_ssm_parameter` |
| `subnet` | `aws_subnet` |
| `swf` | `aws_swf_domain` |
| `synthetics` | `aws_synthetics_canary` |
| `transit_gateway` | `aws_ec2_transit_gateway`, `aws_ec2_transit_gateway_route_table`, `aws_ec2_transit_gateway_vpc_attachment` |
| `vpc` | `aws_vpc` |
| `vpc_endpoint` | `aws_vpc_endpoint` |
| `vpc_peering` | `aws_vpc_peering_connection` |
| `vpn_connection` | `aws_vpn_connection` |
| `vpn_gateway` | `aws_vpn_gateway` |
| `waf` | `aws_waf_byte_match_set`, `aws_waf_geo_match_set`, `aws_waf_ipset`, `aws_waf_rate_based_rule`, `aws_waf_regex_match_set`, `aws_waf_regex_pattern_set`, `aws_waf_rule`, `aws_waf_rule_group`, `aws_waf_size_constraint_set`, `aws_waf_sql_injection_match_set`, `aws_waf_web_acl`, `aws_waf_xss_match_set` |
| `waf_regional` | `aws_wafregional_byte_match_set`, `aws_wafregional_geo_match_set`, `aws_wafregional_ipset`, `aws_wafregional_rate_based_rule`, `aws_wafregional_regex_match_set`, `aws_wafregional_regex_pattern_set`, `aws_wafregional_rule`, `aws_wafregional_rule_group`, `aws_wafregional_size_constraint_set`, `aws_wafregional_sql_injection_match_set`, `aws_wafregional_web_acl`, `aws_wafregional_web_acl_association`, `aws_wafregional_xss_match_set` |
| `wafv2_cloudfront` | `aws_wafv2_ip_set`, `aws_wafv2_regex_pattern_set`, `aws_wafv2_rule_group`, `aws_wafv2_web_acl`, `aws_wafv2_web_acl_association`, `aws_wafv2_web_acl_logging_configuration` |
| `wafv2_regional` | `aws_wafv2_ip_set`, `aws_wafv2_regex_pattern_set`, `aws_wafv2_rule_group`, `aws_wafv2_web_acl`, `aws_wafv2_web_acl_association`, `aws_wafv2_web_acl_logging_configuration` |
| `workspaces` | `aws_workspaces_directory`, `aws_workspaces_ip_group`, `aws_workspaces_workspace` |
| `xray` | `aws_xray_sampling_rule` |

## Azure

`infraharvest import azure` has 4 services, and 7 opt-in.

| Service | Terraform types |
|---|---|
| `management_lock` | `azurerm_management_lock` |
| `resource_graph` | `azurerm_key_vault`, `azurerm_log_analytics_datasource_windows_event`, `azurerm_log_analytics_datasource_windows_performance_counter`, `azurerm_log_analytics_saved_search`, `azurerm_log_analytics_solution`, `azurerm_log_analytics_workspace`, `azurerm_log_analytics_workspace_table_custom_log`, `azurerm_log_analytics_workspace_table_microsoft`, `azurerm_managed_disk`, `azurerm_monitor_action_group`, `azurerm_monitor_alert_prometheus_rule_group`, `azurerm_monitor_data_collection_endpoint`, `azurerm_monitor_data_collection_rule`, `azurerm_monitor_smart_detector_alert_rule`, `azurerm_network_interface`, `azurerm_network_security_group`, `azurerm_route_table`, `azurerm_storage_account`, `azurerm_storage_account_queue_properties`, `azurerm_storage_account_table_properties`, `azurerm_storage_container`, `azurerm_subnet`, `azurerm_virtual_machine_extension`, `azurerm_virtual_network` |
| `security_center_contact` | `azurerm_security_center_contact` |
| `security_center_subscription_pricing` | `azurerm_security_center_subscription_pricing` |
| `managed_identity` (opt-in) | `azurerm_federated_identity_credential`, `azurerm_user_assigned_identity` |
| `network_interface` (opt-in) | `azurerm_network_interface` |
| `network_security_group` (opt-in) | `azurerm_network_security_group`, `azurerm_network_security_rule` |
| `public_ip` (opt-in) | `azurerm_public_ip`, `azurerm_public_ip_prefix` |
| `resource_group` (opt-in) | `azurerm_resource_group` |
| `subnet` (opt-in) | `azurerm_subnet`, `azurerm_subnet_service_endpoint_storage_policy` |
| `virtual_network` (opt-in) | `azurerm_virtual_network` |

## Google Cloud

`infraharvest import google` has 5 services.

| Service | Terraform types |
|---|---|
| `cloud_asset` | `google_artifact_registry_repository`, `google_bigquery_dataset`, `google_bigquery_table`, `google_cloud_run_v2_service`, `google_cloud_tasks_queue`, `google_cloudbuild_trigger`, `google_cloudfunctions2_function`, `google_cloudfunctions_function`, `google_compute_address`, `google_compute_autoscaler`, `google_compute_backend_bucket`, `google_compute_backend_service`, `google_compute_disk`, `google_compute_external_vpn_gateway`, `google_compute_firewall`, `google_compute_forwarding_rule`, `google_compute_global_address`, `google_compute_global_forwarding_rule`, `google_compute_global_network_endpoint_group`, `google_compute_ha_vpn_gateway`, `google_compute_health_check`, `google_compute_http_health_check`, `google_compute_https_health_check`, `google_compute_image`, `google_compute_instance`, `google_compute_instance_group`, `google_compute_instance_group_manager`, `google_compute_instance_template`, `google_compute_interconnect_attachment`, `google_compute_network`, `google_compute_network_endpoint_group`, `google_compute_node_group`, `google_compute_node_template`, `google_compute_packet_mirroring`, `google_compute_region_autoscaler`, `google_compute_region_backend_service`, `google_compute_region_disk`, `google_compute_region_health_check`, `google_compute_region_instance_group_manager`, `google_compute_region_instance_template`, `google_compute_region_network_endpoint_group`, `google_compute_region_security_policy`, `google_compute_region_ssl_policy`, `google_compute_region_target_http_proxy`, `google_compute_region_target_https_proxy`, `google_compute_region_target_tcp_proxy`, `google_compute_region_url_map`, `google_compute_reservation`, `google_compute_resource_policy`, `google_compute_route`, `google_compute_router`, `google_compute_security_policy`, `google_compute_snapshot`, `google_compute_ssl_policy`, `google_compute_subnetwork`, `google_compute_target_http_proxy`, `google_compute_target_https_proxy`, `google_compute_target_instance`, `google_compute_target_pool`, `google_compute_target_ssl_proxy`, `google_compute_target_tcp_proxy`, `google_compute_url_map`, `google_compute_vpn_gateway`, `google_compute_vpn_tunnel`, `google_container_cluster`, `google_container_node_pool`, `google_dataproc_cluster`, `google_dns_managed_zone`, `google_kms_crypto_key`, `google_kms_key_ring`, `google_logging_metric`, `google_monitoring_alert_policy`, `google_monitoring_notification_channel`, `google_monitoring_uptime_check_config`, `google_project`, `google_project_iam_custom_role`, `google_pubsub_subscription`, `google_pubsub_topic`, `google_redis_instance`, `google_secret_manager_regional_secret`, `google_secret_manager_secret`, `google_service_account`, `google_sql_database_instance`, `google_storage_bucket` |
| `cloudsql` | `google_sql_database` |
| `dns` | `google_dns_record_set` |
| `iam` | `google_project_iam_member` |
| `schedulerJobs` | `google_cloud_scheduler_job` |
