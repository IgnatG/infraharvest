// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"maps"
	"slices"
	"sync"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// serviceScope says where a service is listed.
type serviceScope int

const (
	// scopeRegional services are listed in each region.
	scopeRegional serviceScope = iota
	// scopeGlobal services are listed once, in GlobalPseudoRegion.
	scopeGlobal
	// scopeEastOnly services are listed once, in GlobalHomeRegion.
	scopeEastOnly
)

// serviceEntry is one service of the provider: the name --services knows it
// by, where it is listed, how to make a lister for it, and the Terraform
// types the lister lists.
type serviceEntry struct {
	name  string
	scope serviceScope
	// newLister returns a new lister each time: a lister keeps
	// what it listed, and services are listed concurrently.
	newLister func() harvest.Lister
	// types are the Terraform types the lister's code lists, sorted. What
	// the catalogue knows of each, such as its CloudFormation and Resource
	// Explorer types, is in its entry; the types of the catalogue's
	// entries for the service, and their children, are its too (see
	// serviceTypes). TestListedTypesAreDeclared (internal/cli) checks them
	// against the lister's code.
	types []string
}

// listerOf returns a constructor of a new, empty G, for a lister that
// needs no settings.
func listerOf[G any, P interface {
	*G
	harvest.Lister
}]() func() harvest.Lister {
	return func() harvest.Lister { return P(new(G)) }
}

// serviceTable holds the services that have their own lister, sorted by
// name. The services of the catalogue's cloud_control entries are added to
// them (see Services), with the types of those entries.
var serviceTable = []serviceEntry{
	{"accessanalyzer", scopeRegional, listerOf[AccessAnalyzerLister](), []string{"aws_accessanalyzer_analyzer"}},
	{"acm", scopeRegional, listerOf[ACMLister](), []string{"aws_acm_certificate"}},
	{"alb", scopeRegional, listerOf[AlbLister](), []string{
		"aws_lb",
		"aws_lb_listener",
		"aws_lb_listener_certificate",
		"aws_lb_listener_rule",
		"aws_lb_target_group",
		"aws_lb_target_group_attachment",
	}},
	{"api_gateway", scopeRegional, listerOf[APIGatewayLister](), []string{
		"aws_api_gateway_api_key",
		"aws_api_gateway_authorizer",
		"aws_api_gateway_deployment",
		"aws_api_gateway_documentation_part",
		"aws_api_gateway_gateway_response",
		"aws_api_gateway_integration",
		"aws_api_gateway_integration_response",
		"aws_api_gateway_method",
		"aws_api_gateway_method_response",
		"aws_api_gateway_model",
		"aws_api_gateway_resource",
		"aws_api_gateway_rest_api",
		"aws_api_gateway_stage",
		"aws_api_gateway_usage_plan",
		"aws_api_gateway_vpc_link",
	}},
	{"api_gatewayv2", scopeRegional, listerOf[APIGatewayV2Lister](), []string{
		"aws_apigatewayv2_api",
		"aws_apigatewayv2_authorizer",
		"aws_apigatewayv2_integration",
		"aws_apigatewayv2_model",
		"aws_apigatewayv2_route",
		"aws_apigatewayv2_route_response",
		"aws_apigatewayv2_stage",
		"aws_apigatewayv2_vpc_link",
	}},
	{"appsync", scopeRegional, listerOf[AppSyncLister](), []string{"aws_appsync_graphql_api"}},
	{"auto_scaling", scopeRegional, listerOf[AutoScalingLister](), []string{
		"aws_autoscaling_group",
		"aws_launch_configuration",
		"aws_launch_template",
	}},
	{"batch", scopeRegional, listerOf[BatchLister](), []string{
		"aws_batch_compute_environment",
		"aws_batch_job_definition",
		"aws_batch_job_queue",
	}},
	{"budgets", scopeGlobal, listerOf[BudgetsLister](), []string{"aws_budgets_budget"}},
	{"cloud9", scopeRegional, listerOf[Cloud9Lister](), []string{"aws_cloud9_environment_ec2"}},
	{"cloudformation", scopeRegional, listerOf[CloudFormationLister](), []string{
		"aws_cloudformation_stack",
		"aws_cloudformation_stack_set",
		"aws_cloudformation_stack_set_instance",
	}},
	{"cloudfront", scopeGlobal, listerOf[CloudFrontLister](), []string{"aws_cloudfront_cache_policy", "aws_cloudfront_distribution"}},
	{"cloudhsm", scopeRegional, listerOf[CloudHsmLister](), []string{"aws_cloudhsm_v2_cluster", "aws_cloudhsm_v2_hsm"}},
	{"cloudtrail", scopeRegional, listerOf[CloudTrailLister](), []string{"aws_cloudtrail"}},
	{"cloudwatch", scopeRegional, listerOf[CloudWatchLister](), []string{
		"aws_cloudwatch_dashboard",
		"aws_cloudwatch_event_rule",
		"aws_cloudwatch_event_target",
		"aws_cloudwatch_metric_alarm",
	}},
	{"codebuild", scopeRegional, listerOf[CodeBuildLister](), []string{"aws_codebuild_project"}},
	{"codecommit", scopeRegional, listerOf[CodeCommitLister](), []string{"aws_codecommit_approval_rule_template", "aws_codecommit_repository"}},
	{"codepipeline", scopeRegional, listerOf[CodePipelineLister](), []string{"aws_codepipeline", "aws_codepipeline_webhook"}},
	{"cognito", scopeRegional, listerOf[CognitoLister](), []string{
		"aws_cognito_identity_pool",
		"aws_cognito_user_pool",
		"aws_cognito_user_pool_client",
	}},
	{"datapipeline", scopeRegional, listerOf[DataPipelineLister](), []string{"aws_datapipeline_pipeline"}},
	{"devicefarm", scopeRegional, listerOf[DeviceFarmLister](), []string{"aws_devicefarm_project"}},
	{"docdb", scopeRegional, listerOf[DocDBLister](), []string{
		"aws_docdb_cluster",
		"aws_docdb_cluster_instance",
		"aws_docdb_cluster_parameter_group",
		"aws_docdb_subnet_group",
	}},
	{"dx", scopeRegional, listerOf[DirectConnectLister](), []string{
		"aws_dx_connection",
		"aws_dx_private_virtual_interface",
		"aws_dx_public_virtual_interface",
		"aws_dx_transit_virtual_interface",
	}},
	{"dx_gateway", scopeEastOnly, listerOf[DirectConnectGatewayLister](), []string{"aws_dx_gateway"}},
	{"dynamodb", scopeRegional, listerOf[DynamoDbLister](), []string{"aws_dynamodb_table"}},
	{"ebs", scopeRegional, listerOf[EbsLister](), []string{"aws_ebs_volume", "aws_volume_attachment"}},
	{"ec2_instance", scopeRegional, listerOf[Ec2Lister](), []string{"aws_instance"}},
	{"ecr", scopeRegional, listerOf[EcrLister](), []string{
		"aws_ecr_lifecycle_policy",
		"aws_ecr_repository",
		"aws_ecr_repository_policy",
	}},
	{"ecrpublic", scopeGlobal, listerOf[EcrPublicLister](), []string{"aws_ecrpublic_repository"}},
	{"ecs", scopeRegional, listerOf[EcsLister](), []string{
		"aws_ecs_cluster",
		"aws_ecs_service",
		"aws_ecs_task_definition",
	}},
	{"efs", scopeRegional, listerOf[EfsLister](), []string{
		"aws_efs_access_point",
		"aws_efs_file_system",
		"aws_efs_file_system_policy",
		"aws_efs_mount_target",
	}},
	{"eip", scopeRegional, listerOf[ElasticIPLister](), []string{"aws_eip"}},
	{"eni", scopeRegional, listerOf[NetworkInterfaceLister](), []string{"aws_network_interface"}},
	{"eks", scopeRegional, listerOf[EksLister](), []string{
		"aws_eks_access_entry",
		"aws_eks_access_policy_association",
		"aws_eks_addon",
		"aws_eks_cluster",
		"aws_eks_fargate_profile",
		"aws_eks_node_group",
		"aws_eks_pod_identity_association",
	}},
	{"elastic_beanstalk", scopeRegional, listerOf[BeanstalkLister](), []string{"aws_elastic_beanstalk_application", "aws_elastic_beanstalk_environment"}},
	{"elasticache", scopeRegional, listerOf[ElastiCacheLister](), []string{
		"aws_elasticache_cluster",
		"aws_elasticache_parameter_group",
		"aws_elasticache_replication_group",
		"aws_elasticache_subnet_group",
	}},
	{"emr", scopeRegional, listerOf[EmrLister](), []string{"aws_emr_cluster", "aws_emr_security_configuration"}},
	{"es", scopeRegional, listerOf[EsLister](), []string{"aws_elasticsearch_domain", "aws_opensearch_domain"}},
	{"firehose", scopeRegional, listerOf[FirehoseLister](), []string{"aws_kinesis_firehose_delivery_stream"}},
	{"glue", scopeRegional, listerOf[GlueLister](), []string{
		"aws_glue_catalog_database",
		"aws_glue_catalog_table",
		"aws_glue_crawler",
		"aws_glue_job",
		"aws_glue_trigger",
	}},
	{"iam", scopeGlobal, listerOf[IamLister](), []string{
		"aws_iam_access_key",
		"aws_iam_group",
		"aws_iam_group_policy",
		"aws_iam_group_policy_attachment",
		"aws_iam_instance_profile",
		"aws_iam_policy",
		"aws_iam_role",
		"aws_iam_role_policy",
		"aws_iam_role_policy_attachment",
		"aws_iam_user",
		"aws_iam_user_group_membership",
		"aws_iam_user_policy",
		"aws_iam_user_policy_attachment",
	}},
	{"identitystore", scopeRegional, listerOf[IdentityStoreLister](), []string{
		"aws_identitystore_group",
		"aws_identitystore_group_membership",
		"aws_identitystore_user",
	}},
	{"igw", scopeRegional, listerOf[IgwLister](), []string{"aws_internet_gateway"}},
	{"kinesis", scopeRegional, listerOf[KinesisLister](), []string{"aws_kinesis_stream"}},
	{"kms", scopeRegional, listerOf[KmsLister](), []string{"aws_kms_alias", "aws_kms_grant"}},
	{"lambda", scopeRegional, listerOf[LambdaLister](), []string{
		"aws_lambda_event_source_mapping",
		"aws_lambda_function",
		"aws_lambda_function_event_invoke_config",
		"aws_lambda_layer_version",
		"aws_lambda_permission",
	}},
	{"logs", scopeRegional, listerOf[LogsLister](), []string{"aws_cloudwatch_log_group"}},
	{"media_store", scopeRegional, listerOf[MediaStoreLister](), []string{"aws_media_store_container"}},
	{"medialive", scopeRegional, listerOf[MediaLiveLister](), []string{
		"aws_medialive_channel",
		"aws_medialive_input",
		"aws_medialive_input_security_group",
	}},
	{"nacl", scopeRegional, listerOf[NaclLister](), []string{"aws_default_network_acl", "aws_network_acl"}},
	{"nat", scopeRegional, listerOf[NatGatewayLister](), []string{"aws_nat_gateway"}},
	{"organization", scopeGlobal, listerOf[OrganizationLister](), []string{
		"aws_organizations_account",
		"aws_organizations_organization",
		"aws_organizations_organizational_unit",
		"aws_organizations_policy",
		"aws_organizations_policy_attachment",
	}},
	{"rds", scopeRegional, listerOf[RDSLister](), []string{
		"aws_db_cluster_snapshot",
		"aws_db_event_subscription",
		"aws_db_instance",
		"aws_db_option_group",
		"aws_db_parameter_group",
		"aws_db_proxy",
		"aws_db_snapshot",
		"aws_db_subnet_group",
		"aws_rds_cluster",
		"aws_rds_cluster_instance",
		"aws_rds_global_cluster",
	}},
	{"redshift", scopeRegional, listerOf[RedshiftLister](), []string{
		"aws_redshift_cluster",
		"aws_redshift_event_subscription",
		"aws_redshift_parameter_group",
		"aws_redshift_snapshot_schedule",
		"aws_redshift_snapshot_schedule_association",
		"aws_redshift_subnet_group",
	}},
	{"route53", scopeGlobal, listerOf[Route53Lister](), []string{
		"aws_route53_health_check",
		"aws_route53_record",
		"aws_route53_zone",
	}},
	{"route_table", scopeRegional, listerOf[RouteTableLister](), []string{
		"aws_main_route_table_association",
		"aws_route_table",
		"aws_route_table_association",
	}},
	{"s3", scopeRegional, listerOf[S3Lister](), []string{"aws_s3_bucket", "aws_s3_bucket_policy"}},
	{"secretsmanager", scopeRegional, listerOf[SecretsManagerLister](), []string{"aws_secretsmanager_secret"}},
	{"securityhub", scopeRegional, listerOf[SecurityhubLister](), []string{
		"aws_securityhub_account",
		"aws_securityhub_member",
		"aws_securityhub_standards_subscription",
	}},
	{"ses", scopeRegional, listerOf[SesLister](), []string{
		"aws_ses_configuration_set",
		"aws_ses_domain_identity",
		"aws_ses_email_identity",
		"aws_ses_receipt_rule",
		"aws_ses_receipt_rule_set",
		"aws_ses_template",
	}},
	{"sfn", scopeRegional, listerOf[SfnLister](), []string{"aws_sfn_activity", "aws_sfn_state_machine"}},
	{"sg", scopeRegional, listerOf[SecurityLister](), []string{
		"aws_security_group",
		"aws_vpc_security_group_egress_rule",
		"aws_vpc_security_group_ingress_rule",
	}},
	{"sns", scopeRegional, listerOf[SnsLister](), []string{"aws_sns_topic", "aws_sns_topic_subscription"}},
	{"sqs", scopeRegional, listerOf[SqsLister](), []string{"aws_sqs_queue"}},
	{"ssm", scopeRegional, listerOf[SsmLister](), []string{"aws_ssm_parameter"}},
	{"subnet", scopeRegional, listerOf[SubnetLister](), []string{"aws_subnet"}},
	{"swf", scopeRegional, listerOf[SWFLister](), []string{"aws_swf_domain"}},
	{"transit_gateway", scopeRegional, listerOf[TransitGatewayLister](), []string{
		"aws_ec2_transit_gateway",
		"aws_ec2_transit_gateway_route_table",
		"aws_ec2_transit_gateway_vpc_attachment",
	}},
	{"vpc", scopeRegional, listerOf[VpcLister](), []string{"aws_vpc"}},
	{"vpc_endpoint", scopeRegional, listerOf[VpcEndpointLister](), []string{"aws_vpc_endpoint"}},
	{"waf", scopeGlobal, listerOf[WafLister](), []string{
		"aws_waf_byte_match_set",
		"aws_waf_geo_match_set",
		"aws_waf_ipset",
		"aws_waf_rate_based_rule",
		"aws_waf_regex_match_set",
		"aws_waf_regex_pattern_set",
		"aws_waf_rule",
		"aws_waf_rule_group",
		"aws_waf_size_constraint_set",
		"aws_waf_sql_injection_match_set",
		"aws_waf_web_acl",
		"aws_waf_xss_match_set",
	}},
	{"waf_regional", scopeRegional, listerOf[WafRegionalLister](), []string{
		"aws_wafregional_byte_match_set",
		"aws_wafregional_geo_match_set",
		"aws_wafregional_ipset",
		"aws_wafregional_rate_based_rule",
		"aws_wafregional_regex_match_set",
		"aws_wafregional_regex_pattern_set",
		"aws_wafregional_rule",
		"aws_wafregional_rule_group",
		"aws_wafregional_size_constraint_set",
		"aws_wafregional_sql_injection_match_set",
		"aws_wafregional_web_acl",
		"aws_wafregional_web_acl_association",
		"aws_wafregional_xss_match_set",
	}},
	// Web ACLs for CloudFront live in us-east-1, in WAFv2's CLOUDFRONT
	// scope.
	{"wafv2_cloudfront", scopeEastOnly, func() harvest.Lister { return NewWafv2CloudfrontLister() }, []string{
		"aws_wafv2_ip_set",
		"aws_wafv2_regex_pattern_set",
		"aws_wafv2_rule_group",
		"aws_wafv2_web_acl",
		"aws_wafv2_web_acl_association",
		"aws_wafv2_web_acl_logging_configuration",
	}},
	{"wafv2_regional", scopeRegional, func() harvest.Lister { return NewWafv2RegionalLister() }, []string{
		"aws_wafv2_ip_set",
		"aws_wafv2_regex_pattern_set",
		"aws_wafv2_rule_group",
		"aws_wafv2_web_acl",
		"aws_wafv2_web_acl_association",
		"aws_wafv2_web_acl_logging_configuration",
	}},
	{"workspaces", scopeRegional, listerOf[WorkspacesLister](), []string{
		"aws_workspaces_directory",
		"aws_workspaces_ip_group",
		"aws_workspaces_workspace",
	}},
	{"xray", scopeRegional, listerOf[XrayLister](), []string{"aws_xray_sampling_rule"}},
}

// servicesOfScope returns the names of the services of serviceTable listed
// in scope, in the table's order.
func servicesOfScope(scope serviceScope) []string {
	var names []string
	for _, s := range serviceTable {
		if s.scope == scope {
			names = append(names, s.name)
		}
	}
	return names
}

// serviceTypes returns the Terraform types the service called name lists
// with cat, sorted: those its lister declares (see serviceEntry.types),
// those of cat's entries for the service, which for a Cloud Control service
// are all of them, and the children cat names for any of these, and
// theirs.
func serviceTypes(cat *catalogue.Catalogue, name string) []string {
	found := map[string]bool{}
	for _, s := range serviceTable {
		if s.name == name {
			for _, typ := range s.types {
				found[typ] = true
			}
		}
	}
	for _, typ := range cat.Types() {
		if e, _ := cat.Entry(typ); e.Service == name {
			found[typ] = true
		}
	}
	for queue := slices.Collect(maps.Keys(found)); len(queue) > 0; queue = queue[1:] {
		e, ok := cat.Entry(queue[0])
		if !ok {
			continue
		}
		for _, child := range e.Children {
			if !found[child] {
				found[child] = true
				queue = append(queue, child)
			}
		}
	}
	return slices.Sorted(maps.Keys(found))
}

// listedTypes are the Terraform types the services list with the built-in
// catalogue, each with the first service by name that lists it (see
// harvest.Profile.ListedTypes). They are worked out once, as the built-in
// catalogue doesn't change; callers must not modify the answer.
var listedTypes = sync.OnceValue(func() map[string]string {
	cat := awsCatalogue()
	names := slices.Collect(maps.Keys(cloudControlServicesOf(cat)))
	for _, s := range serviceTable {
		names = append(names, s.name)
	}
	slices.Sort(names)
	listed := map[string]string{}
	for _, name := range slices.Compact(names) {
		for _, typ := range serviceTypes(cat, name) {
			if _, ok := listed[typ]; !ok {
				listed[typ] = name
			}
		}
	}
	return listed
})
