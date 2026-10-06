// Copyright 2018 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aws

import (
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/pkg/errors"
)

type AWSProvider struct { //nolint
	terraformutils.Provider
	region  string
	profile string
	// roleARN, if set, is a role to assume for every call, such as a
	// read-only role in one account of an organization.
	roleARN string
}

const GlobalRegion = "aws-global"
const MainRegionPublicPartition = "us-east-1"
const NoRegion = ""

// SupportedGlobalResources should be bound to a default region. AWS doesn't specify in which region default services are
// placed (see  https://docs.aws.amazon.com/general/latest/gr/rande.html), so we shouldn't assume any region as well
var SupportedGlobalResources = []string{
	"budgets",
	"cloudfront",
	"ecrpublic",
	"iam",
	"organization",
	"route53",
	"waf",
}

// SupportedEastOnlyResources should be bound to us-east-1 region only, and does not work in any other region.
var SupportedEastOnlyResources = []string{
	"wafv2_cloudfront",
}

func (p AWSProvider) GetProviderData(arg ...string) map[string]interface{} {
	awsConfig := map[string]interface{}{}

	if p.region == GlobalRegion {
		awsConfig["region"] = MainRegionPublicPartition // For TF to workaround terraform-providers/terraform-provider-aws#1043
	} else if p.region != NoRegion {
		awsConfig["region"] = p.region
	}
	// Terraform assumes the same role as the import, from the credentials of
	// whoever plans.
	if p.roleARN != "" {
		awsConfig["assume_role"] = map[string]interface{}{"role_arn": p.roleARN}
	}

	return map[string]interface{}{
		"provider": map[string]interface{}{
			"aws": awsConfig,
		},
	}
}

// Init takes the region, the profile and, optionally, a role to assume.
// They configure each service's SDK client (see AWSService.generateConfig)
// and Terraform (see GetProviderData and TerraformEnv), never the process
// environment: several accounts and regions can import in one process.
func (p *AWSProvider) Init(args []string) error {
	p.region = args[0]
	p.profile = args[1]
	if len(args) > 2 {
		p.roleARN = args[2]
	}
	return nil
}

func (p *AWSProvider) GetName() string {
	return "aws"
}

func (p *AWSProvider) InitService(serviceName string, verbose bool) error {
	var isSupported bool
	if _, isSupported = p.GetSupportedService()[serviceName]; !isSupported {
		return errors.New("aws: " + serviceName + " not supported service")
	}
	p.Service = p.GetSupportedService()[serviceName]
	p.Service.SetName(serviceName)
	p.Service.SetVerbose(verbose)
	p.Service.SetProviderName(p.GetName())
	p.Service.SetArgs(p.serviceArgs())
	return nil
}

// GetSupportedService returns the services the provider lists: those with a
// lister of their own, and those listed through Cloud Control (see
// cloudControlServices).
func (p *AWSProvider) GetSupportedService() map[string]terraformutils.ServiceGenerator {
	services := map[string]terraformutils.ServiceGenerator{
		"accessanalyzer":    &AwsFacade{service: &AccessAnalyzerGenerator{}},
		"acm":               &AwsFacade{service: &ACMGenerator{}},
		"alb":               &AwsFacade{service: &AlbGenerator{}},
		"api_gateway":       &AwsFacade{service: &APIGatewayGenerator{}},
		"api_gatewayv2":     &AwsFacade{service: &APIGatewayV2Generator{}},
		"appsync":           &AwsFacade{service: &AppSyncGenerator{}},
		"auto_scaling":      &AwsFacade{service: &AutoScalingGenerator{}},
		"batch":             &AwsFacade{service: &BatchGenerator{}},
		"budgets":           &AwsFacade{service: &BudgetsGenerator{}},
		"cloud9":            &AwsFacade{service: &Cloud9Generator{}},
		"cloudformation":    &AwsFacade{service: &CloudFormationGenerator{}},
		"cloudfront":        &AwsFacade{service: &CloudFrontGenerator{}},
		"cloudhsm":          &AwsFacade{service: &CloudHsmGenerator{}},
		"cloudtrail":        &AwsFacade{service: &CloudTrailGenerator{}},
		"cloudwatch":        &AwsFacade{service: &CloudWatchGenerator{}},
		"codebuild":         &AwsFacade{service: &CodeBuildGenerator{}},
		"codecommit":        &AwsFacade{service: &CodeCommitGenerator{}},
		"codedeploy":        &AwsFacade{service: &CodeDeployGenerator{}},
		"codepipeline":      &AwsFacade{service: &CodePipelineGenerator{}},
		"cognito":           &AwsFacade{service: &CognitoGenerator{}},
		"config":            &AwsFacade{service: &ConfigGenerator{}},
		"customer_gateway":  &AwsFacade{service: &CustomerGatewayGenerator{}},
		"datapipeline":      &AwsFacade{service: &DataPipelineGenerator{}},
		"devicefarm":        &AwsFacade{service: &DeviceFarmGenerator{}},
		"docdb":             &AwsFacade{service: &DocDBGenerator{}},
		"dx":                &AwsFacade{service: &DirectConnectGenerator{}},
		"dynamodb":          &AwsFacade{service: &DynamoDbGenerator{}},
		"ebs":               &AwsFacade{service: &EbsGenerator{}},
		"ec2_instance":      &AwsFacade{service: &Ec2Generator{}},
		"ecr":               &AwsFacade{service: &EcrGenerator{}},
		"ecrpublic":         &AwsFacade{service: &EcrPublicGenerator{}},
		"ecs":               &AwsFacade{service: &EcsGenerator{}},
		"efs":               &AwsFacade{service: &EfsGenerator{}},
		"eks":               &AwsFacade{service: &EksGenerator{}},
		"eip":               &AwsFacade{service: &ElasticIPGenerator{}},
		"elasticache":       &AwsFacade{service: &ElastiCacheGenerator{}},
		"elastic_beanstalk": &AwsFacade{service: &BeanstalkGenerator{}},
		"elb":               &AwsFacade{service: &ElbGenerator{}},
		"emr":               &AwsFacade{service: &EmrGenerator{}},
		"eni":               &AwsFacade{service: &EniGenerator{}},
		"es":                &AwsFacade{service: &EsGenerator{}},
		"firehose":          &AwsFacade{service: &FirehoseGenerator{}},
		"glue":              &AwsFacade{service: &GlueGenerator{}},
		"iam":               &AwsFacade{service: &IamGenerator{}},
		"identitystore":     &AwsFacade{service: &IdentityStoreGenerator{}},
		"igw":               &AwsFacade{service: &IgwGenerator{}},
		"iot":               &AwsFacade{service: &IotGenerator{}},
		"kinesis":           &AwsFacade{service: &KinesisGenerator{}},
		"kms":               &AwsFacade{service: &KmsGenerator{}},
		"lambda":            &AwsFacade{service: &LambdaGenerator{}},
		"logs":              &AwsFacade{service: &LogsGenerator{}},
		"media_package":     &AwsFacade{service: &MediaPackageGenerator{}},
		"media_store":       &AwsFacade{service: &MediaStoreGenerator{}},
		"medialive":         &AwsFacade{service: &MediaLiveGenerator{}},
		"mq":                &AwsFacade{service: &MQGenerator{}},
		"msk":               &AwsFacade{service: &MskGenerator{}},
		"nacl":              &AwsFacade{service: &NaclGenerator{}},
		"nat":               &AwsFacade{service: &NatGatewayGenerator{}},
		"opsworks":          &AwsFacade{service: &OpsworksGenerator{}},
		"organization":      &AwsFacade{service: &OrganizationGenerator{}},
		"qldb":              &AwsFacade{service: &QLDBGenerator{}},
		"rds":               &AwsFacade{service: &RDSGenerator{}},
		"redshift":          &AwsFacade{service: &RedshiftGenerator{}},
		"resourcegroups":    &AwsFacade{service: &ResourceGroupsGenerator{}},
		"route53":           &AwsFacade{service: &Route53Generator{}},
		"route_table":       &AwsFacade{service: &RouteTableGenerator{}},
		"s3":                &AwsFacade{service: &S3Generator{}},
		"secretsmanager":    &AwsFacade{service: &SecretsManagerGenerator{}},
		"securityhub":       &AwsFacade{service: &SecurityhubGenerator{}},
		"servicecatalog":    &AwsFacade{service: &ServiceCatalogGenerator{}},
		"ses":               &AwsFacade{service: &SesGenerator{}},
		"sfn":               &AwsFacade{service: &SfnGenerator{}},
		"sg":                &AwsFacade{service: &SecurityGenerator{}},
		"sqs":               &AwsFacade{service: &SqsGenerator{}},
		"sns":               &AwsFacade{service: &SnsGenerator{}},
		"ssm":               &AwsFacade{service: &SsmGenerator{}},
		"subnet":            &AwsFacade{service: &SubnetGenerator{}},
		"swf":               &AwsFacade{service: &SWFGenerator{}},
		"transit_gateway":   &AwsFacade{service: &TransitGatewayGenerator{}},
		"waf":               &AwsFacade{service: &WafGenerator{}},
		"waf_regional":      &AwsFacade{service: &WafRegionalGenerator{}},
		"wafv2_cloudfront":  &AwsFacade{service: NewWafv2CloudfrontGenerator()},
		"wafv2_regional":    &AwsFacade{service: NewWafv2RegionalGenerator()},
		"vpc":               &AwsFacade{service: &VpcGenerator{}},
		"vpc_endpoint":      &AwsFacade{service: &VpcEndpointGenerator{}},
		"vpc_peering":       &AwsFacade{service: &VpcPeeringConnectionGenerator{}},
		"vpn_connection":    &AwsFacade{service: &VpnConnectionGenerator{}},
		"vpn_gateway":       &AwsFacade{service: &VpnGatewayGenerator{}},
		"workspaces":        &AwsFacade{service: &WorkspacesGenerator{}},
		"xray":              &AwsFacade{service: &XrayGenerator{}},
	}
	for name := range cloudControlServices {
		services[name] = &AwsFacade{service: newCloudControlGenerator(name)}
	}
	return services
}

func StringValue(value *string) string {
	if value != nil {
		return *value
	}
	return ""
}

// serviceArgs are the arguments of the provider's services: region,
// profile and the role to assume.
func (p *AWSProvider) serviceArgs() map[string]interface{} {
	return map[string]interface{}{
		"region":                 p.region,
		"profile":                p.profile,
		"role_arn":               p.roleARN,
		"skip_region_validation": true,
	}
}
