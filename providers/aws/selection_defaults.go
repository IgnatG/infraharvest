// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/IgnatG/infraharvest/terraformutils"
)

// Reasons the default selection leaves AWS resources out.
const (
	reasonDefaultVPC      = "default VPC, which AWS creates in every region"
	reasonDefaultNetwork  = "part of the default VPC, which AWS creates in every region"
	reasonDefaultSG       = "default security group, which AWS creates with every VPC"
	reasonDefaultNACL     = "default network ACL, which AWS creates with every VPC"
	reasonServiceLinked   = "service-linked role, which AWS manages for a service"
	reasonLambdaLogGroups = "log group Lambda creates for a function"
	reasonCreatedByAWS    = "created by AWS for the account, such as a default event bus or workgroup"
)

// ExcludedByDefault names the resources the default selection leaves out,
// by "type id", with the reason: resources AWS creates and manages itself,
// such as the default VPC and its subnets, default security groups and
// network ACLs, service-linked roles, Lambda's log groups, and the
// defaults of services listed through Cloud Control; and what
// CloudFormation stacks manage, which would otherwise have two owners.
func (p *AWSProvider) ExcludedByDefault(ctx context.Context, resources []terraformutils.Resource) (map[string]string, error) {
	excluded := map[string]string{}
	network := false
	for _, r := range resources {
		id := r.InstanceState.ID
		switch typ := r.InstanceInfo.Type; {
		case typ == "aws_default_network_acl":
			excluded[typ+" "+id] = reasonDefaultNACL
		case strings.HasPrefix(typ, "aws_iam_role") && strings.HasPrefix(id, "AWSServiceRoleFor"):
			excluded[typ+" "+id] = reasonServiceLinked
		case typ == "aws_cloudwatch_log_group" && strings.HasPrefix(id, "/aws/lambda/"):
			excluded[typ+" "+id] = reasonLambdaLogGroups
		case createdByAWS(typ, id):
			excluded[typ+" "+id] = reasonCreatedByAWS
		case typ == "aws_vpc", typ == "aws_subnet", typ == "aws_security_group", typ == "aws_security_group_rule",
			typ == "aws_route_table", typ == "aws_internet_gateway":
			network = true
		}
	}
	if len(resources) == 0 {
		return excluded, nil
	}
	config, err := p.config(ctx)
	if err != nil {
		return excluded, err
	}
	stacks, stacksErr := p.cloudFormationManaged(ctx, config)
	for _, r := range resources {
		key := r.InstanceInfo.Type + " " + r.InstanceState.ID
		if _, ok := excluded[key]; ok {
			continue
		}
		stack, ok := stacks[r.InstanceState.ID]
		if !ok {
			if id, importable := p.ImportID(r); importable {
				stack, ok = stacks[id]
			}
		}
		if ok {
			excluded[key] = reasonCloudFormation(stack)
		}
	}
	if !network || p.region == GlobalRegion {
		return excluded, stacksErr
	}
	defaults, err := p.defaultNetwork(ctx, config)
	err = errors.Join(stacksErr, err)
	for _, r := range resources {
		id := r.InstanceState.ID
		reason := ""
		switch r.InstanceInfo.Type {
		case "aws_vpc":
			if defaults.vpcs[id] {
				reason = reasonDefaultVPC
			}
		case "aws_subnet", "aws_route_table", "aws_internet_gateway":
			if defaults.parts[id] {
				reason = reasonDefaultNetwork
			}
		case "aws_security_group":
			if defaults.securityGroups[id] {
				reason = reasonDefaultSG
			}
		case "aws_security_group_rule":
			if defaults.securityGroups[r.InstanceState.Attributes["security_group_id"]] {
				reason = reasonDefaultSG
			}
		}
		if reason != "" {
			excluded[r.InstanceInfo.Type+" "+id] = reason
		}
	}
	return excluded, err
}

// createdByAWS reports whether AWS created a resource listed through Cloud
// Control for the account (see cloudControlType.CreatedByAWS).
func createdByAWS(typ, id string) bool {
	t, ok := cloudControlTypeOf(typ)
	return ok && t.CreatedByAWS != nil && t.CreatedByAWS(id)
}

// defaultNetwork is what AWS creates on its own: default VPCs, their
// subnets, main route tables and internet gateways, and the default
// security group of every VPC.
type defaultNetwork struct {
	vpcs, parts, securityGroups map[string]bool
}

// config returns the AWS configuration the provider's services list with.
func (p *AWSProvider) config(ctx context.Context) (aws.Config, error) {
	service := &AWSService{}
	service.SetArgs(p.serviceArgs())
	service.SetContext(ctx)
	return service.generateConfig()
}

func (p *AWSProvider) defaultNetwork(ctx context.Context, config aws.Config) (defaultNetwork, error) {
	d := defaultNetwork{vpcs: map[string]bool{}, parts: map[string]bool{}, securityGroups: map[string]bool{}}
	svc := ec2.NewFromConfig(config)
	var errs []error

	vpcs := ec2.NewDescribeVpcsPaginator(svc, &ec2.DescribeVpcsInput{Filters: []ec2types.Filter{ec2Filter("is-default", "true")}}, stopOnDuplicateToken)
	for vpcs.HasMorePages() {
		page, err := vpcs.NextPage(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("default VPCs: %w", err))
			break
		}
		for _, v := range page.Vpcs {
			d.vpcs[aws.ToString(v.VpcId)] = true
		}
	}
	defaultVPCs := make([]string, 0, len(d.vpcs))
	for id := range d.vpcs {
		defaultVPCs = append(defaultVPCs, id)
	}

	if len(defaultVPCs) > 0 {
		subnets := ec2.NewDescribeSubnetsPaginator(svc, &ec2.DescribeSubnetsInput{Filters: []ec2types.Filter{ec2Filter("vpc-id", defaultVPCs...)}}, stopOnDuplicateToken)
		for subnets.HasMorePages() {
			page, err := subnets.NextPage(ctx)
			if err != nil {
				errs = append(errs, fmt.Errorf("default subnets: %w", err))
				break
			}
			for _, s := range page.Subnets {
				d.parts[aws.ToString(s.SubnetId)] = true
			}
		}
		tables := ec2.NewDescribeRouteTablesPaginator(svc, &ec2.DescribeRouteTablesInput{Filters: []ec2types.Filter{ec2Filter("vpc-id", defaultVPCs...)}}, stopOnDuplicateToken)
		for tables.HasMorePages() {
			page, err := tables.NextPage(ctx)
			if err != nil {
				errs = append(errs, fmt.Errorf("default route tables: %w", err))
				break
			}
			for _, t := range page.RouteTables {
				d.parts[aws.ToString(t.RouteTableId)] = true
			}
		}
		gateways := ec2.NewDescribeInternetGatewaysPaginator(svc, &ec2.DescribeInternetGatewaysInput{Filters: []ec2types.Filter{ec2Filter("attachment.vpc-id", defaultVPCs...)}}, stopOnDuplicateToken)
		for gateways.HasMorePages() {
			page, err := gateways.NextPage(ctx)
			if err != nil {
				errs = append(errs, fmt.Errorf("default internet gateways: %w", err))
				break
			}
			for _, g := range page.InternetGateways {
				d.parts[aws.ToString(g.InternetGatewayId)] = true
			}
		}
	}

	groups := ec2.NewDescribeSecurityGroupsPaginator(svc, &ec2.DescribeSecurityGroupsInput{Filters: []ec2types.Filter{ec2Filter("group-name", "default")}}, stopOnDuplicateToken)
	for groups.HasMorePages() {
		page, err := groups.NextPage(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("default security groups: %w", err))
			break
		}
		for _, g := range page.SecurityGroups {
			d.securityGroups[aws.ToString(g.GroupId)] = true
		}
	}
	return d, errors.Join(errs...)
}

func ec2Filter(name string, values ...string) ec2types.Filter {
	return ec2types.Filter{Name: aws.String(name), Values: values}
}
