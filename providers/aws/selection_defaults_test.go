// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"reflect"
	"testing"

	"github.com/IgnatG/infraharvest/terraformutils"
)

const ec2NS = `xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"`

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
	}
	return `<Response><Errors><Error><Code>Unexpected</Code></Error></Errors></Response>`
}

func TestExcludedByDefault(t *testing.T) {
	useFakeAPI(t, defaultNetworkAPI)
	p := &AWSProvider{region: "us-east-1"}
	resources := []terraformutils.Resource{
		terraformutils.NewSimpleResource("vpc-default", "vpc-default", "aws_vpc", "aws", nil),
		terraformutils.NewSimpleResource("vpc-0abc1234", "main", "aws_vpc", "aws", nil),
		terraformutils.NewSimpleResource("subnet-default-a", "a", "aws_subnet", "aws", nil),
		terraformutils.NewSimpleResource("subnet-0own", "b", "aws_subnet", "aws", nil),
		terraformutils.NewSimpleResource("rtb-default", "main", "aws_route_table", "aws", nil),
		terraformutils.NewSimpleResource("igw-default", "gw", "aws_internet_gateway", "aws", nil),
		terraformutils.NewSimpleResource("sg-default-own", "default", "aws_security_group", "aws", nil),
		terraformutils.NewResource("sgrule-1", "sgrule-1", "aws_security_group_rule", "aws", map[string]string{"security_group_id": "sg-default-own"}, nil, nil),
		terraformutils.NewSimpleResource("acl-1", "acl-1", "aws_default_network_acl", "aws", nil),
		terraformutils.NewSimpleResource("AWSServiceRoleForECS", "ecs", "aws_iam_role", "aws", nil),
		terraformutils.NewSimpleResource("/aws/lambda/fn", "fn", "aws_cloudwatch_log_group", "aws", nil),
		terraformutils.NewSimpleResource("/app/web", "web", "aws_cloudwatch_log_group", "aws", nil),
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

// Global services have no network to ask about.
func TestExcludedByDefaultGlobal(t *testing.T) {
	p := &AWSProvider{region: GlobalRegion}
	excluded, err := p.ExcludedByDefault(t.Context(), []terraformutils.Resource{
		terraformutils.NewSimpleResource("AWSServiceRoleForECS", "ecs", "aws_iam_role", "aws", nil),
		terraformutils.NewSimpleResource("app", "app", "aws_iam_role", "aws", nil),
	})
	if err != nil || len(excluded) != 1 {
		t.Errorf("got %v, %v; want the service-linked role only", excluded, err)
	}
}
