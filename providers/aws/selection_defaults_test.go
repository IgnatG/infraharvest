// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/terraformutils"
)

const ec2NS = `xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"`

// noStacks answers ListStacks for a region without CloudFormation stacks.
const noStacks = `<ListStacksResponse xmlns="http://cloudformation.amazonaws.com/doc/2010-05-15/"><ListStacksResult><StackSummaries/></ListStacksResult></ListStacksResponse>`

// defaultNetworkAPI answers like a region whose default VPC is vpc-default.
func defaultNetworkAPI(call apiCall) string {
	switch call.Op {
	case "DescribeVpcs":
		return `<DescribeVpcsResponse ` + ec2NS + `><vpcSet><item><vpcId>vpc-default</vpcId></item></vpcSet></DescribeVpcsResponse>`
	case "DescribeSubnets":
		return `<DescribeSubnetsResponse ` + ec2NS + `><subnetSet><item><subnetId>subnet-default-a</subnetId></item></subnetSet></DescribeSubnetsResponse>`
	case "DescribeRouteTables":
		return `<DescribeRouteTablesResponse ` + ec2NS + `><routeTableSet><item><routeTableId>rtb-default</routeTableId></item></routeTableSet></DescribeRouteTablesResponse>`
	case "DescribeInternetGateways":
		return `<DescribeInternetGatewaysResponse ` + ec2NS + `><internetGatewaySet><item><internetGatewayId>igw-default</internetGatewayId></item></internetGatewaySet></DescribeInternetGatewaysResponse>`
	case "DescribeSecurityGroups":
		return `<DescribeSecurityGroupsResponse ` + ec2NS + `><securityGroupInfo><item><groupId>sg-default-own</groupId></item></securityGroupInfo></DescribeSecurityGroupsResponse>`
	case "ListStacks":
		return noStacks
	}
	return `<Response><Errors><Error><Code>Unexpected</Code></Error></Errors></Response>`
}

func TestExcludedByDefault(t *testing.T) {
	useFakeAPI(t, defaultNetworkAPI)
	p := &AWSProvider{region: "us-east-1"}
	resources := []terraformutils.Resource{
		terraformutils.NewSimpleResource("vpc-default", "vpc-default", "aws_vpc", "aws"),
		terraformutils.NewSimpleResource("vpc-0abc1234", "main", "aws_vpc", "aws"),
		terraformutils.NewSimpleResource("subnet-default-a", "a", "aws_subnet", "aws"),
		terraformutils.NewSimpleResource("subnet-0own", "b", "aws_subnet", "aws"),
		terraformutils.NewSimpleResource("rtb-default", "main", "aws_route_table", "aws"),
		terraformutils.NewSimpleResource("igw-default", "gw", "aws_internet_gateway", "aws"),
		terraformutils.NewSimpleResource("sg-default-own", "default", "aws_security_group", "aws"),
		terraformutils.NewResource("sgrule-1", "sgrule-1", "aws_security_group_rule", "aws", map[string]string{"security_group_id": "sg-default-own"}),
		terraformutils.NewSimpleResource("acl-1", "acl-1", "aws_default_network_acl", "aws"),
		terraformutils.NewSimpleResource("AWSServiceRoleForECS", "ecs", "aws_iam_role", "aws"),
		terraformutils.NewSimpleResource("/aws/lambda/fn", "fn", "aws_cloudwatch_log_group", "aws"),
		terraformutils.NewSimpleResource("/app/web", "web", "aws_cloudwatch_log_group", "aws"),
	}

	excluded, err := p.ExcludedByDefault(t.Context(), resources)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"aws_vpc vpc-default":                     reasonDefaultVPC,
		"aws_subnet subnet-default-a":             reasonDefaultNetwork,
		"aws_route_table rtb-default":             reasonDefaultNetwork,
		"aws_internet_gateway igw-default":        reasonDefaultNetwork,
		"aws_security_group sg-default-own":       reasonDefaultSG,
		"aws_security_group_rule sgrule-1":        reasonDefaultSG,
		"aws_default_network_acl acl-1":           reasonDefaultNACL,
		"aws_iam_role AWSServiceRoleForECS":       reasonServiceLinked,
		"aws_cloudwatch_log_group /aws/lambda/fn": reasonLambdaLogGroups,
	}
	if !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
}

// Global services have no network to ask about; their stacks are in every
// enabled region.
func TestExcludedByDefaultGlobal(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "DescribeRegions":
			return `<DescribeRegionsResponse ` + ec2NS + `><regionInfo><item><regionName>eu-west-2</regionName></item></regionInfo></DescribeRegionsResponse>`
		case "ListStacks":
			return noStacks
		}
		t.Errorf("unexpected call %s", call.Op)
		return ""
	})
	p := &AWSProvider{region: GlobalRegion}
	excluded, err := p.ExcludedByDefault(t.Context(), []terraformutils.Resource{
		terraformutils.NewSimpleResource("AWSServiceRoleForECS", "ecs", "aws_iam_role", "aws"),
		terraformutils.NewSimpleResource("app", "app", "aws_iam_role", "aws"),
	})
	if err != nil || len(excluded) != 1 {
		t.Errorf("got %v, %v; want the service-linked role only", excluded, err)
	}
}

// What a CloudFormation stack manages is left out, by its ID or the ID
// Terraform imports it with; what no stack manages isn't.
func TestExcludedByDefaultCloudFormation(t *testing.T) {
	const ns = `xmlns="http://cloudformation.amazonaws.com/doc/2010-05-15/"`
	var stacks []string
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListStacks":
			if !strings.Contains(call.Body, "StackStatusFilter.member.1=") || strings.Contains(call.Body, "DELETE_COMPLETE&") {
				t.Errorf("ListStacks must ask for live stacks only: %s", call.Body)
			}
			return `<ListStacksResponse ` + ns + `><ListStacksResult><StackSummaries>` +
				`<member><StackId>arn:aws:cloudformation:us-east-1:123456789012:stack/app/1</StackId><StackName>app</StackName><StackStatus>CREATE_COMPLETE</StackStatus><CreationTime>2026-01-01T00:00:00Z</CreationTime></member>` +
				`</StackSummaries></ListStacksResult></ListStacksResponse>`
		case "ListStackResources":
			stacks = append(stacks, call.Body)
			return `<ListStackResourcesResponse ` + ns + `><ListStackResourcesResult><StackResourceSummaries>` +
				`<member><LogicalResourceId>Assets</LogicalResourceId><PhysicalResourceId>app-assets</PhysicalResourceId><ResourceType>AWS::S3::Bucket</ResourceType><ResourceStatus>CREATE_COMPLETE</ResourceStatus><LastUpdatedTimestamp>2026-01-01T00:00:00Z</LastUpdatedTimestamp></member>` +
				`<member><LogicalResourceId>Cluster</LogicalResourceId><PhysicalResourceId>apps</PhysicalResourceId><ResourceType>AWS::ECS::Cluster</ResourceType><ResourceStatus>CREATE_COMPLETE</ResourceStatus><LastUpdatedTimestamp>2026-01-01T00:00:00Z</LastUpdatedTimestamp></member>` +
				`</StackResourceSummaries></ListStackResourcesResult></ListStackResourcesResponse>`
		}
		t.Errorf("unexpected call %s", call.Op)
		return ""
	})
	p := &AWSProvider{region: "us-east-1"}

	excluded, err := p.ExcludedByDefault(t.Context(), []terraformutils.Resource{
		terraformutils.NewSimpleResource("app-assets", "assets", "aws_s3_bucket", "aws"),
		// The lister records the cluster's ARN; Terraform imports it by name.
		terraformutils.NewSimpleResource("arn:aws:ecs:us-east-1:123456789012:cluster/apps", "apps", "aws_ecs_cluster", "aws"),
		terraformutils.NewSimpleResource("own-bucket", "own", "aws_s3_bucket", "aws"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"aws_s3_bucket app-assets": reasonCloudFormation("app"),
		"aws_ecs_cluster arn:aws:ecs:us-east-1:123456789012:cluster/apps": reasonCloudFormation("app"),
	}
	if !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
	if len(stacks) != 1 || !strings.Contains(stacks[0], "StackName=arn") {
		t.Errorf("ListStackResources: %q, want one call by stack ID", stacks)
	}
}
