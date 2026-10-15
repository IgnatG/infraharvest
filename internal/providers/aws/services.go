// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
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
// by, where it is listed, and how to make a lister for it.
type serviceEntry struct {
	name  string
	scope serviceScope
	// newLister returns a new lister each time: a lister keeps
	// what it listed, and services are listed concurrently.
	newLister func() harvest.Lister
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
// them (see Services).
var serviceTable = []serviceEntry{
	{"accessanalyzer", scopeRegional, listerOf[AccessAnalyzerLister]()},
	{"acm", scopeRegional, listerOf[ACMLister]()},
	{"alb", scopeRegional, listerOf[AlbLister]()},
	{"api_gateway", scopeRegional, listerOf[APIGatewayLister]()},
	{"api_gatewayv2", scopeRegional, listerOf[APIGatewayV2Lister]()},
	{"appsync", scopeRegional, listerOf[AppSyncLister]()},
	{"auto_scaling", scopeRegional, listerOf[AutoScalingLister]()},
	{"batch", scopeRegional, listerOf[BatchLister]()},
	{"budgets", scopeGlobal, listerOf[BudgetsLister]()},
	{"cloud9", scopeRegional, listerOf[Cloud9Lister]()},
	{"cloudformation", scopeRegional, listerOf[CloudFormationLister]()},
	{"cloudfront", scopeGlobal, listerOf[CloudFrontLister]()},
	{"cloudhsm", scopeRegional, listerOf[CloudHsmLister]()},
	{"cloudtrail", scopeRegional, listerOf[CloudTrailLister]()},
	{"cloudwatch", scopeRegional, listerOf[CloudWatchLister]()},
	{"codebuild", scopeRegional, listerOf[CodeBuildLister]()},
	{"codecommit", scopeRegional, listerOf[CodeCommitLister]()},
	{"codepipeline", scopeRegional, listerOf[CodePipelineLister]()},
	{"cognito", scopeRegional, listerOf[CognitoLister]()},
	{"datapipeline", scopeRegional, listerOf[DataPipelineLister]()},
	{"devicefarm", scopeRegional, listerOf[DeviceFarmLister]()},
	{"docdb", scopeRegional, listerOf[DocDBLister]()},
	{"dx", scopeRegional, listerOf[DirectConnectLister]()},
	{"dynamodb", scopeRegional, listerOf[DynamoDbLister]()},
	{"ebs", scopeRegional, listerOf[EbsLister]()},
	{"ec2_instance", scopeRegional, listerOf[Ec2Lister]()},
	{"ecr", scopeRegional, listerOf[EcrLister]()},
	{"ecrpublic", scopeGlobal, listerOf[EcrPublicLister]()},
	{"ecs", scopeRegional, listerOf[EcsLister]()},
	{"efs", scopeRegional, listerOf[EfsLister]()},
	{"eip", scopeRegional, listerOf[ElasticIPLister]()},
	{"eks", scopeRegional, listerOf[EksLister]()},
	{"elastic_beanstalk", scopeRegional, listerOf[BeanstalkLister]()},
	{"elasticache", scopeRegional, listerOf[ElastiCacheLister]()},
	{"emr", scopeRegional, listerOf[EmrLister]()},
	{"es", scopeRegional, listerOf[EsLister]()},
	{"firehose", scopeRegional, listerOf[FirehoseLister]()},
	{"glue", scopeRegional, listerOf[GlueLister]()},
	{"iam", scopeGlobal, listerOf[IamLister]()},
	{"identitystore", scopeRegional, listerOf[IdentityStoreLister]()},
	{"igw", scopeRegional, listerOf[IgwLister]()},
	{"kinesis", scopeRegional, listerOf[KinesisLister]()},
	{"kms", scopeRegional, listerOf[KmsLister]()},
	{"lambda", scopeRegional, listerOf[LambdaLister]()},
	{"logs", scopeRegional, listerOf[LogsLister]()},
	{"media_store", scopeRegional, listerOf[MediaStoreLister]()},
	{"medialive", scopeRegional, listerOf[MediaLiveLister]()},
	{"nacl", scopeRegional, listerOf[NaclLister]()},
	{"nat", scopeRegional, listerOf[NatGatewayLister]()},
	{"organization", scopeGlobal, listerOf[OrganizationLister]()},
	{"rds", scopeRegional, listerOf[RDSLister]()},
	{"redshift", scopeRegional, listerOf[RedshiftLister]()},
	{"route53", scopeGlobal, listerOf[Route53Lister]()},
	{"route_table", scopeRegional, listerOf[RouteTableLister]()},
	{"s3", scopeRegional, listerOf[S3Lister]()},
	{"secretsmanager", scopeRegional, listerOf[SecretsManagerLister]()},
	{"securityhub", scopeRegional, listerOf[SecurityhubLister]()},
	{"ses", scopeRegional, listerOf[SesLister]()},
	{"sfn", scopeRegional, listerOf[SfnLister]()},
	{"sg", scopeRegional, listerOf[SecurityLister]()},
	{"sns", scopeRegional, listerOf[SnsLister]()},
	{"sqs", scopeRegional, listerOf[SqsLister]()},
	{"ssm", scopeRegional, listerOf[SsmLister]()},
	{"subnet", scopeRegional, listerOf[SubnetLister]()},
	{"swf", scopeRegional, listerOf[SWFLister]()},
	{"transit_gateway", scopeRegional, listerOf[TransitGatewayLister]()},
	{"vpc", scopeRegional, listerOf[VpcLister]()},
	{"vpc_endpoint", scopeRegional, listerOf[VpcEndpointLister]()},
	{"waf", scopeGlobal, listerOf[WafLister]()},
	{"waf_regional", scopeRegional, listerOf[WafRegionalLister]()},
	// Web ACLs for CloudFront live in us-east-1, in WAFv2's CLOUDFRONT
	// scope.
	{"wafv2_cloudfront", scopeEastOnly, func() harvest.Lister { return NewWafv2CloudfrontLister() }},
	{"wafv2_regional", scopeRegional, func() harvest.Lister { return NewWafv2RegionalLister() }},
	{"workspaces", scopeRegional, listerOf[WorkspacesLister]()},
	{"xray", scopeRegional, listerOf[XrayLister]()},
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
