// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"

	"github.com/IgnatG/infraharvest/terraformutils"
)

// arnResource is where the ARNs of a Terraform type's resources name them:
// the ARN's service, and the resource type its resource part starts with,
// "" if the part is the name alone (an S3 bucket's).
type arnResource struct {
	service, resourceType string
}

// arnResources are the ARN service and resource type of the taggable
// Terraform types whose IDs are names, not ARNs. Resources of the other
// types are tagged only if their ID, import ID or arn attribute is an ARN
// the Resource Groups Tagging API lists.
var arnResources = map[string]arnResource{
	"aws_acm_certificate":                    {"acm", "certificate"},
	"aws_api_gateway_rest_api":               {"apigateway", "restapis"},
	"aws_apigatewayv2_api":                   {"apigateway", "apis"},
	"aws_athena_workgroup":                   {"athena", "workgroup"},
	"aws_autoscaling_group":                  {"autoscaling", "autoScalingGroup"},
	"aws_backup_vault":                       {"backup", "backup-vault"},
	"aws_cloudfront_distribution":            {"cloudfront", "distribution"},
	"aws_cloudtrail":                         {"cloudtrail", "trail"},
	"aws_cloudwatch_event_bus":               {"events", "event-bus"},
	"aws_cloudwatch_event_rule":              {"events", "rule"},
	"aws_cloudwatch_log_group":               {"logs", "log-group"},
	"aws_cloudwatch_metric_alarm":            {"cloudwatch", "alarm"},
	"aws_codebuild_project":                  {"codebuild", "project"},
	"aws_codecommit_repository":              {"codecommit", ""},
	"aws_codepipeline":                       {"codepipeline", ""},
	"aws_cognito_user_pool":                  {"cognito-idp", "userpool"},
	"aws_customer_gateway":                   {"ec2", "customer-gateway"},
	"aws_db_instance":                        {"rds", "db"},
	"aws_db_option_group":                    {"rds", "og"},
	"aws_db_parameter_group":                 {"rds", "pg"},
	"aws_db_subnet_group":                    {"rds", "subgrp"},
	"aws_docdb_cluster":                      {"rds", "cluster"},
	"aws_dynamodb_table":                     {"dynamodb", "table"},
	"aws_ebs_snapshot":                       {"ec2", "snapshot"},
	"aws_ebs_volume":                         {"ec2", "volume"},
	"aws_ec2_transit_gateway":                {"ec2", "transit-gateway"},
	"aws_ec2_transit_gateway_vpc_attachment": {"ec2", "transit-gateway-attachment"},
	"aws_ecr_repository":                     {"ecr", "repository"},
	"aws_ecs_cluster":                        {"ecs", "cluster"},
	"aws_efs_file_system":                    {"elasticfilesystem", "file-system"},
	"aws_eip":                                {"ec2", "elastic-ip"},
	"aws_eks_cluster":                        {"eks", "cluster"},
	"aws_elastic_beanstalk_application":      {"elasticbeanstalk", "application"},
	"aws_elasticache_cluster":                {"elasticache", "cluster"},
	"aws_elasticache_parameter_group":        {"elasticache", "parametergroup"},
	"aws_elasticache_replication_group":      {"elasticache", "replicationgroup"},
	"aws_elasticache_subnet_group":           {"elasticache", "subnetgroup"},
	"aws_elasticsearch_domain":               {"es", "domain"},
	"aws_elb":                                {"elasticloadbalancing", "loadbalancer"},
	"aws_emr_cluster":                        {"elasticmapreduce", "cluster"},
	"aws_flow_log":                           {"ec2", "vpc-flow-log"},
	"aws_glue_crawler":                       {"glue", "crawler"},
	"aws_glue_job":                           {"glue", "job"},
	"aws_iam_role":                           {"iam", "role"},
	"aws_iam_user":                           {"iam", "user"},
	"aws_instance":                           {"ec2", "instance"},
	"aws_internet_gateway":                   {"ec2", "internet-gateway"},
	"aws_kinesis_stream":                     {"kinesis", "stream"},
	"aws_kms_key":                            {"kms", "key"},
	"aws_lambda_function":                    {"lambda", "function"},
	"aws_launch_template":                    {"ec2", "launch-template"},
	"aws_mq_broker":                          {"mq", "broker"},
	"aws_nat_gateway":                        {"ec2", "natgateway"},
	"aws_neptune_cluster":                    {"rds", "cluster"},
	"aws_network_acl":                        {"ec2", "network-acl"},
	"aws_network_interface":                  {"ec2", "network-interface"},
	"aws_opensearch_domain":                  {"es", "domain"},
	"aws_placement_group":                    {"ec2", "placement-group"},
	"aws_rds_cluster":                        {"rds", "cluster"},
	"aws_rds_cluster_parameter_group":        {"rds", "cluster-pg"},
	"aws_redshift_cluster":                   {"redshift", "cluster"},
	"aws_route53_zone":                       {"route53", "hostedzone"},
	"aws_route_table":                        {"ec2", "route-table"},
	"aws_s3_bucket":                          {"s3", ""},
	"aws_security_group":                     {"ec2", "security-group"},
	"aws_sqs_queue":                          {"sqs", ""},
	"aws_ssm_parameter":                      {"ssm", "parameter"},
	"aws_subnet":                             {"ec2", "subnet"},
	"aws_vpc":                                {"ec2", "vpc"},
	"aws_vpc_endpoint":                       {"ec2", "vpc-endpoint"},
	"aws_vpc_peering_connection":             {"ec2", "vpc-peering-connection"},
	"aws_vpn_connection":                     {"ec2", "vpn-connection"},
	"aws_vpn_gateway":                        {"ec2", "vpn-gateway"},
}

// tagIndex finds the tags of resources by ARN, or by the service, resource
// type and name an ARN is made of.
type tagIndex struct {
	byARN map[string]map[string]string
	// byName holds the ARNs of each service, resource type and name; a
	// name more than one ARN shares can't say which is meant.
	byName map[arnResource]map[string][]string
}

func newTagIndex() *tagIndex {
	return &tagIndex{byARN: map[string]map[string]string{}, byName: map[arnResource]map[string][]string{}}
}

// add records the tags of the resource arn names.
func (x *tagIndex) add(arn string, tags map[string]string) {
	x.byARN[arn] = tags
	// arn:partition:service:region:account:resource
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 {
		return
	}
	service, resource := parts[2], parts[5]
	resourceType, name := splitARNResource(resource)
	key := arnResource{service, resourceType}
	if x.byName[key] == nil {
		x.byName[key] = map[string][]string{}
	}
	names := []string{name}
	// A role or user path, an Auto Scaling group's ID, an API Gateway
	// stage: the name is the last part.
	if i := strings.LastIndexAny(name, "/:"); i >= 0 && i < len(name)-1 {
		names = append(names, name[i+1:])
	}
	for _, n := range names {
		if !slices.Contains(x.byName[key][n], arn) {
			x.byName[key][n] = append(x.byName[key][n], arn)
		}
	}
}

// splitARNResource splits an ARN's resource part into its resource type
// and name: role/admin, function:name, /restapis/id, or a bucket name
// alone.
func splitARNResource(resource string) (resourceType, name string) {
	trimmed := strings.TrimPrefix(resource, "/")
	i := strings.IndexAny(trimmed, "/:")
	if i < 0 {
		return "", resource
	}
	return trimmed[:i], trimmed[i+1:]
}

// lookup returns the tags of r, whose import ID is importID.
func (x *tagIndex) lookup(r terraformutils.Resource, importID string) (map[string]string, bool) {
	candidates := []string{r.InstanceState.ID, importID, r.InstanceState.Attributes["arn"]}
	for _, c := range candidates {
		if tags, ok := x.byARN[c]; ok && c != "" {
			return tags, true
		}
	}
	key, ok := arnResources[r.InstanceInfo.Type]
	if !ok {
		return nil, false
	}
	for _, c := range candidates[:2] {
		if c == "" {
			continue
		}
		names := []string{c, strings.TrimPrefix(c, "/")}
		// A queue's ID is its URL, which ends with its name.
		if strings.Contains(c, "://") {
			names = append(names, c[strings.LastIndex(c, "/")+1:])
		}
		for _, n := range names {
			if arns := x.byName[key][n]; len(arns) == 1 {
				return x.byARN[arns[0]], true
			}
		}
	}
	return nil, false
}

// Tags reads the tags of the listed resources from the Resource Groups
// Tagging API of the region they were listed in: us-east-1 for the global
// services, where it lists CloudFront distributions and Route 53 zones.
// It returns them by "type id".
func (p *AWSProvider) Tags(ctx context.Context, resources []terraformutils.Resource) (map[string]map[string]string, error) {
	config, err := p.config(ctx)
	if err != nil {
		return nil, err
	}
	if p.region == GlobalRegion {
		config = config.Copy()
		config.Region = "us-east-1"
	}
	index := newTagIndex()
	pages := resourcegroupstaggingapi.NewGetResourcesPaginator(resourcegroupstaggingapi.NewFromConfig(config), &resourcegroupstaggingapi.GetResourcesInput{
		ResourcesPerPage: aws.Int32(100),
	}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("tags in %s: %w", config.Region, err)
		}
		for _, m := range page.ResourceTagMappingList {
			tags := make(map[string]string, len(m.Tags))
			for _, t := range m.Tags {
				tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
			}
			index.add(aws.ToString(m.ResourceARN), tags)
		}
	}
	found := map[string]map[string]string{}
	for _, r := range resources {
		importID, _ := p.ImportID(r)
		if tags, ok := index.lookup(r, importID); ok && len(tags) > 0 {
			found[r.InstanceInfo.Type+" "+r.InstanceState.ID] = tags
		}
	}
	return found, nil
}
