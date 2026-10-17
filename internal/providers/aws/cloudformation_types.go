// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// cloudFormationTypes are the Terraform types a resource of a
// CloudFormation type can be listed as with the stack's physical ID, by
// CloudFormation type. A listed resource that has the physical ID of a
// stack's resource is managed by the stack only when its type is one of
// them, so that a hand-made role is not taken for the instance profile of
// the same name a stack made. A type also covers the types whose names
// start with its own and an underscore, and the children the catalogue
// names for it, which hold parts of its configuration under the same ID,
// such as an S3 bucket's versioning; so the list names the others only,
// such as a repository's lifecycle policy. The catalogue's cloud_control
// entries add their CloudFormation types to it (see stackTypes).
//
// A CloudFormation type that is not here, such as a custom resource,
// manages whatever has its physical ID, whatever its type: a resource that
// may be a stack's is never imported. A type added here must list every
// Terraform type its physical ID names.
var cloudFormationTypes = map[string][]string{
	"AWS::AccessAnalyzer::Analyzer":                 {"aws_accessanalyzer_analyzer"},
	"AWS::ApiGateway::ApiKey":                       {"aws_api_gateway_api_key"},
	"AWS::ApiGateway::Authorizer":                   {"aws_api_gateway_authorizer"},
	"AWS::ApiGateway::Deployment":                   {"aws_api_gateway_deployment", "aws_api_gateway_stage"},
	"AWS::ApiGateway::DocumentationPart":            {"aws_api_gateway_documentation_part"},
	"AWS::ApiGateway::GatewayResponse":              {"aws_api_gateway_gateway_response"},
	"AWS::ApiGateway::Method":                       {"aws_api_gateway_method", "aws_api_gateway_method_response", "aws_api_gateway_integration", "aws_api_gateway_integration_response"},
	"AWS::ApiGateway::Model":                        {"aws_api_gateway_model"},
	"AWS::ApiGateway::Resource":                     {"aws_api_gateway_resource"},
	"AWS::ApiGateway::RestApi":                      {"aws_api_gateway_rest_api"},
	"AWS::ApiGateway::Stage":                        {"aws_api_gateway_stage", "aws_api_gateway_method_settings"},
	"AWS::ApiGateway::UsagePlan":                    {"aws_api_gateway_usage_plan"},
	"AWS::ApiGateway::VpcLink":                      {"aws_api_gateway_vpc_link"},
	"AWS::ApiGatewayV2::Api":                        {"aws_apigatewayv2_api"},
	"AWS::ApiGatewayV2::Authorizer":                 {"aws_apigatewayv2_authorizer"},
	"AWS::ApiGatewayV2::Integration":                {"aws_apigatewayv2_integration"},
	"AWS::ApiGatewayV2::Model":                      {"aws_apigatewayv2_model"},
	"AWS::ApiGatewayV2::Route":                      {"aws_apigatewayv2_route"},
	"AWS::ApiGatewayV2::RouteResponse":              {"aws_apigatewayv2_route_response"},
	"AWS::ApiGatewayV2::Stage":                      {"aws_apigatewayv2_stage"},
	"AWS::ApiGatewayV2::VpcLink":                    {"aws_apigatewayv2_vpc_link"},
	"AWS::AppSync::GraphQLApi":                      {"aws_appsync_graphql_api"},
	"AWS::AutoScaling::AutoScalingGroup":            {"aws_autoscaling_group"},
	"AWS::AutoScaling::LaunchConfiguration":         {"aws_launch_configuration"},
	"AWS::Batch::ComputeEnvironment":                {"aws_batch_compute_environment"},
	"AWS::Batch::JobDefinition":                     {"aws_batch_job_definition"},
	"AWS::Batch::JobQueue":                          {"aws_batch_job_queue"},
	"AWS::Budgets::Budget":                          {"aws_budgets_budget"},
	"AWS::CertificateManager::Certificate":          {"aws_acm_certificate"},
	"AWS::CloudFormation::Stack":                    {"aws_cloudformation_stack"},
	"AWS::CloudFormation::StackSet":                 {"aws_cloudformation_stack_set"},
	"AWS::CloudFront::CachePolicy":                  {"aws_cloudfront_cache_policy"},
	"AWS::CloudFront::Distribution":                 {"aws_cloudfront_distribution"},
	"AWS::CloudTrail::Trail":                        {"aws_cloudtrail"},
	"AWS::CloudWatch::Alarm":                        {"aws_cloudwatch_metric_alarm"},
	"AWS::CloudWatch::Dashboard":                    {"aws_cloudwatch_dashboard"},
	"AWS::CodeBuild::Project":                       {"aws_codebuild_project"},
	"AWS::CodeCommit::Repository":                   {"aws_codecommit_repository"},
	"AWS::CodePipeline::Pipeline":                   {"aws_codepipeline"},
	"AWS::CodePipeline::Webhook":                    {"aws_codepipeline_webhook"},
	"AWS::Cognito::IdentityPool":                    {"aws_cognito_identity_pool"},
	"AWS::Cognito::UserPool":                        {"aws_cognito_user_pool"},
	"AWS::Cognito::UserPoolClient":                  {"aws_cognito_user_pool_client"},
	"AWS::DocDB::DBCluster":                         {"aws_docdb_cluster", "aws_rds_cluster"},
	"AWS::DocDB::DBClusterParameterGroup":           {"aws_docdb_cluster_parameter_group", "aws_rds_cluster_parameter_group"},
	"AWS::DocDB::DBInstance":                        {"aws_docdb_cluster_instance", "aws_rds_cluster_instance", "aws_db_instance"},
	"AWS::DocDB::DBSubnetGroup":                     {"aws_docdb_subnet_group", "aws_db_subnet_group"},
	"AWS::DynamoDB::GlobalTable":                    {"aws_dynamodb_table"},
	"AWS::DynamoDB::Table":                          {"aws_dynamodb_table"},
	"AWS::EC2::EIP":                                 {"aws_eip"},
	"AWS::EC2::Instance":                            {"aws_instance"},
	"AWS::EC2::InternetGateway":                     {"aws_internet_gateway"},
	"AWS::EC2::LaunchTemplate":                      {"aws_launch_template"},
	"AWS::EC2::NatGateway":                          {"aws_nat_gateway"},
	"AWS::EC2::NetworkAcl":                          {"aws_network_acl"},
	"AWS::EC2::NetworkInterface":                    {"aws_network_interface"},
	"AWS::EC2::RouteTable":                          {"aws_route_table"},
	"AWS::EC2::SecurityGroup":                       {"aws_security_group"},
	"AWS::EC2::SecurityGroupEgress":                 {"aws_security_group_rule", "aws_vpc_security_group_egress_rule"},
	"AWS::EC2::SecurityGroupIngress":                {"aws_security_group_rule", "aws_vpc_security_group_ingress_rule"},
	"AWS::EC2::Subnet":                              {"aws_subnet"},
	"AWS::EC2::SubnetRouteTableAssociation":         {"aws_route_table_association"},
	"AWS::EC2::TransitGateway":                      {"aws_ec2_transit_gateway"},
	"AWS::EC2::TransitGatewayAttachment":            {"aws_ec2_transit_gateway_vpc_attachment"},
	"AWS::EC2::TransitGatewayRouteTable":            {"aws_ec2_transit_gateway_route_table"},
	"AWS::EC2::TransitGatewayVpcAttachment":         {"aws_ec2_transit_gateway_vpc_attachment"},
	"AWS::EC2::VPC":                                 {"aws_vpc"},
	"AWS::EC2::VPCEndpoint":                         {"aws_vpc_endpoint"},
	"AWS::EC2::Volume":                              {"aws_ebs_volume"},
	"AWS::EC2::VolumeAttachment":                    {"aws_volume_attachment"},
	"AWS::ECR::PublicRepository":                    {"aws_ecrpublic_repository"},
	"AWS::ECR::Repository":                          {"aws_ecr_repository", "aws_ecr_lifecycle_policy"},
	"AWS::ECS::Cluster":                             {"aws_ecs_cluster"},
	"AWS::ECS::ClusterCapacityProviderAssociations": {"aws_ecs_cluster_capacity_providers"},
	"AWS::ECS::Service":                             {"aws_ecs_service"},
	"AWS::ECS::TaskDefinition":                      {"aws_ecs_task_definition"},
	"AWS::EFS::AccessPoint":                         {"aws_efs_access_point"},
	"AWS::EFS::FileSystem":                          {"aws_efs_file_system", "aws_efs_backup_policy", "aws_efs_replication_configuration"},
	"AWS::EFS::MountTarget":                         {"aws_efs_mount_target"},
	"AWS::EKS::AccessEntry":                         {"aws_eks_access_entry", "aws_eks_access_policy_association"},
	"AWS::EKS::Addon":                               {"aws_eks_addon"},
	"AWS::EKS::Cluster":                             {"aws_eks_cluster"},
	"AWS::EKS::FargateProfile":                      {"aws_eks_fargate_profile"},
	"AWS::EKS::Nodegroup":                           {"aws_eks_node_group"},
	"AWS::EKS::PodIdentityAssociation":              {"aws_eks_pod_identity_association"},
	"AWS::EMR::Cluster":                             {"aws_emr_cluster"},
	"AWS::EMR::SecurityConfiguration":               {"aws_emr_security_configuration"},
	"AWS::ElastiCache::CacheCluster":                {"aws_elasticache_cluster"},
	"AWS::ElastiCache::ParameterGroup":              {"aws_elasticache_parameter_group"},
	"AWS::ElastiCache::ReplicationGroup":            {"aws_elasticache_replication_group"},
	"AWS::ElastiCache::SubnetGroup":                 {"aws_elasticache_subnet_group"},
	"AWS::ElasticBeanstalk::Application":            {"aws_elastic_beanstalk_application"},
	"AWS::ElasticBeanstalk::Environment":            {"aws_elastic_beanstalk_environment"},
	"AWS::ElasticLoadBalancingV2::Listener":         {"aws_lb_listener", "aws_alb_listener"},
	"AWS::ElasticLoadBalancingV2::ListenerRule":     {"aws_lb_listener_rule", "aws_alb_listener_rule"},
	"AWS::ElasticLoadBalancingV2::LoadBalancer":     {"aws_lb", "aws_alb"},
	"AWS::ElasticLoadBalancingV2::TargetGroup":      {"aws_lb_target_group", "aws_alb_target_group"},
	"AWS::Elasticsearch::Domain":                    {"aws_elasticsearch_domain", "aws_opensearch_domain"},
	"AWS::Events::Rule":                             {"aws_cloudwatch_event_rule"},
	"AWS::Glue::Crawler":                            {"aws_glue_crawler"},
	"AWS::Glue::Database":                           {"aws_glue_catalog_database"},
	"AWS::Glue::Job":                                {"aws_glue_job"},
	"AWS::Glue::Table":                              {"aws_glue_catalog_table"},
	"AWS::Glue::Trigger":                            {"aws_glue_trigger"},
	"AWS::IAM::AccessKey":                           {"aws_iam_access_key"},
	"AWS::IAM::Group":                               {"aws_iam_group"},
	"AWS::IAM::InstanceProfile":                     {"aws_iam_instance_profile"},
	"AWS::IAM::ManagedPolicy":                       {"aws_iam_policy"},
	"AWS::IAM::OIDCProvider":                        {"aws_iam_openid_connect_provider"},
	"AWS::IAM::Policy":                              {"aws_iam_role_policy", "aws_iam_user_policy", "aws_iam_group_policy"},
	"AWS::IAM::Role":                                {"aws_iam_role"},
	"AWS::IAM::RolePolicy":                          {"aws_iam_role_policy"},
	"AWS::IAM::SAMLProvider":                        {"aws_iam_saml_provider"},
	"AWS::IAM::ServiceLinkedRole":                   {"aws_iam_service_linked_role", "aws_iam_role"},
	"AWS::IAM::User":                                {"aws_iam_user"},
	"AWS::IAM::UserToGroupAddition":                 {"aws_iam_user_group_membership", "aws_iam_group_membership"},
	"AWS::KMS::Alias":                               {"aws_kms_alias"},
	"AWS::KMS::Key":                                 {"aws_kms_key", "aws_kms_external_key", "aws_kms_replica_key", "aws_kms_replica_external_key"},
	"AWS::KMS::ReplicaKey":                          {"aws_kms_replica_key", "aws_kms_replica_external_key"},
	"AWS::Kinesis::Stream":                          {"aws_kinesis_stream"},
	"AWS::KinesisFirehose::DeliveryStream":          {"aws_kinesis_firehose_delivery_stream"},
	"AWS::Lambda::EventInvokeConfig":                {"aws_lambda_function_event_invoke_config"},
	"AWS::Lambda::EventSourceMapping":               {"aws_lambda_event_source_mapping"},
	"AWS::Lambda::Function":                         {"aws_lambda_function"},
	"AWS::Lambda::LayerVersion":                     {"aws_lambda_layer_version"},
	"AWS::Lambda::Permission":                       {"aws_lambda_permission"},
	"AWS::Logs::LogGroup":                           {"aws_cloudwatch_log_group", "aws_cloudwatch_log_data_protection_policy"},
	"AWS::OpenSearchService::Domain":                {"aws_opensearch_domain", "aws_elasticsearch_domain"},
	"AWS::RDS::DBCluster":                           {"aws_rds_cluster", "aws_docdb_cluster"},
	"AWS::RDS::DBClusterParameterGroup":             {"aws_rds_cluster_parameter_group", "aws_docdb_cluster_parameter_group"},
	"AWS::RDS::DBInstance":                          {"aws_db_instance", "aws_rds_cluster_instance", "aws_docdb_cluster_instance"},
	"AWS::RDS::DBParameterGroup":                    {"aws_db_parameter_group"},
	"AWS::RDS::DBProxy":                             {"aws_db_proxy"},
	"AWS::RDS::DBSubnetGroup":                       {"aws_db_subnet_group", "aws_docdb_subnet_group"},
	"AWS::RDS::EventSubscription":                   {"aws_db_event_subscription"},
	"AWS::RDS::GlobalCluster":                       {"aws_rds_global_cluster"},
	"AWS::RDS::OptionGroup":                         {"aws_db_option_group"},
	"AWS::Redshift::Cluster":                        {"aws_redshift_cluster"},
	"AWS::Redshift::ClusterParameterGroup":          {"aws_redshift_parameter_group"},
	"AWS::Redshift::ClusterSubnetGroup":             {"aws_redshift_subnet_group"},
	"AWS::Redshift::EventSubscription":              {"aws_redshift_event_subscription"},
	"AWS::Route53::HealthCheck":                     {"aws_route53_health_check"},
	"AWS::Route53::HostedZone":                      {"aws_route53_zone", "aws_route53_hosted_zone_dnssec"},
	"AWS::Route53::RecordSet":                       {"aws_route53_record"},
	"AWS::Route53::RecordSetGroup":                  {"aws_route53_record"},
	"AWS::S3::Bucket":                               {"aws_s3_bucket"},
	"AWS::S3::BucketPolicy":                         {"aws_s3_bucket_policy"},
	"AWS::SES::ConfigurationSet":                    {"aws_ses_configuration_set"},
	"AWS::SES::EmailIdentity":                       {"aws_ses_email_identity", "aws_ses_domain_identity", "aws_sesv2_email_identity"},
	"AWS::SES::ReceiptRule":                         {"aws_ses_receipt_rule"},
	"AWS::SES::ReceiptRuleSet":                      {"aws_ses_receipt_rule_set"},
	"AWS::SES::Template":                            {"aws_ses_template"},
	"AWS::SNS::Subscription":                        {"aws_sns_topic_subscription"},
	"AWS::SNS::Topic":                               {"aws_sns_topic"},
	"AWS::SNS::TopicPolicy":                         {"aws_sns_topic_policy"},
	"AWS::SQS::Queue":                               {"aws_sqs_queue"},
	"AWS::SQS::QueuePolicy":                         {"aws_sqs_queue_policy"},
	"AWS::SSM::Parameter":                           {"aws_ssm_parameter"},
	"AWS::SecretsManager::ResourcePolicy":           {"aws_secretsmanager_secret_policy"},
	"AWS::SecretsManager::RotationSchedule":         {"aws_secretsmanager_secret_rotation"},
	"AWS::SecretsManager::Secret":                   {"aws_secretsmanager_secret"},
	"AWS::SecurityHub::Hub":                         {"aws_securityhub_account"},
	"AWS::StepFunctions::Activity":                  {"aws_sfn_activity"},
	"AWS::StepFunctions::StateMachine":              {"aws_sfn_state_machine"},
	"AWS::WAFv2::IPSet":                             {"aws_wafv2_ip_set"},
	"AWS::WAFv2::LoggingConfiguration":              {"aws_wafv2_web_acl_logging_configuration"},
	"AWS::WAFv2::RegexPatternSet":                   {"aws_wafv2_regex_pattern_set"},
	"AWS::WAFv2::RuleGroup":                         {"aws_wafv2_rule_group"},
	"AWS::WAFv2::WebACL":                            {"aws_wafv2_web_acl"},
	"AWS::WAFv2::WebACLAssociation":                 {"aws_wafv2_web_acl_association"},
	"AWS::XRay::SamplingRule":                       {"aws_xray_sampling_rule"},
}

// stackResource is a resource a CloudFormation stack manages: the stack's
// name, and the resource's CloudFormation type.
type stackResource struct {
	stack, cloudFormationType string
}

// stackTypes returns cloudFormationTypes with the CloudFormation types of
// cat's cloud_control entries added, each to the Terraform type of its
// entry.
func stackTypes(cat *catalogue.Catalogue) map[string][]string {
	types := make(map[string][]string, len(cloudFormationTypes))
	for cfn, terraform := range cloudFormationTypes {
		types[cfn] = slices.Clone(terraform)
	}
	for _, typ := range cat.Types() {
		if e, _ := cat.Entry(typ); e.CloudControl != nil && !slices.Contains(types[e.CloudControl.Type], typ) {
			types[e.CloudControl.Type] = append(types[e.CloudControl.Type], typ)
		}
	}
	return types
}

// managingStack returns the stack of managed, the resources of stacks
// that have one physical ID, that manages a listed resource of the
// Terraform type typ with that ID, and whether one does: the first whose
// CloudFormation type, by types (see stackTypes), can be listed as typ, or
// that types doesn't know.
func managingStack(managed []stackResource, cat *catalogue.Catalogue, types map[string][]string, typ string) (string, bool) {
	for _, m := range managed {
		terraform, known := types[m.cloudFormationType]
		if !known || slices.ContainsFunc(terraform, func(t string) bool { return coversType(cat, t, typ) }) {
			return m.stack, true
		}
	}
	return "", false
}

// coversType reports whether a resource of the Terraform type parent may
// share its ID with one of typ that holds a part of it: typ is parent,
// starts with parent and an underscore, as aws_s3_bucket_policy does, or
// is one of the children the catalogue names for parent.
func coversType(cat *catalogue.Catalogue, parent, typ string) bool {
	if typ == parent || strings.HasPrefix(typ, parent+"_") {
		return true
	}
	e, ok := cat.Entry(parent)
	return ok && slices.Contains(e.Children, typ)
}
