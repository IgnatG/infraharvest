// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Reasons the default selection leaves AWS resources out.
const (
	reasonDefaultVPC     = "default VPC, which AWS creates in every region"
	reasonDefaultNetwork = "part of the default VPC, which AWS creates in every region"
	reasonDefaultSG      = "default security group, which AWS creates with every VPC"
	reasonEKSClusterSG   = "security group EKS creates for a cluster and deletes with it"
)

// eksClusterTag is the tag EKS gives the security group it creates for a
// cluster; the aws: prefix is AWS's own, which no one else can set.
const eksClusterTag = "tags.aws:eks:cluster-name"

// ExcludedByDefault names the resources the default selection leaves out,
// by "type id", with the reason, that it takes asking AWS to find: the
// default VPC and its subnets, route tables and internet gateways, default
// security groups and the security groups EKS creates for clusters, with
// their rules, which AWS creates itself; and what
// CloudFormation stacks manage, which would otherwise have two owners. The
// catalogue's skip rules leave out what IDs tell, such as service-linked
// roles.
func (p *Provider) ExcludedByDefault(ctx context.Context, resources []harvest.Resource) (map[string]string, error) {
	excluded := map[string]string{}
	network := false
	for _, r := range resources {
		switch r.Address.Type {
		case "aws_vpc", "aws_subnet", "aws_security_group", "aws_security_group_rule", "aws_vpc_security_group_ingress_rule", "aws_vpc_security_group_egress_rule", "aws_route_table", "aws_internet_gateway":
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
		key := r.Address.Type + " " + r.Observed.ID
		stack, ok := stacks[r.Observed.ID]
		if !ok {
			if id, importable := importID(r); importable {
				stack, ok = stacks[id]
			}
		}
		if ok {
			excluded[key] = reasonCloudFormation(stack)
		}
	}
	if !network || p.region == GlobalPseudoRegion {
		return excluded, stacksErr
	}
	defaults, err := p.defaultNetwork(ctx, config)
	err = errors.Join(stacksErr, err)
	eksGroups := map[string]bool{}
	for _, r := range resources {
		if r.Address.Type == "aws_security_group" && r.Observed.Attributes[eksClusterTag] != "" {
			eksGroups[r.Observed.ID] = true
		}
	}
	for _, r := range resources {
		id := r.Observed.ID
		reason := ""
		switch r.Address.Type {
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
			} else if eksGroups[id] {
				reason = reasonEKSClusterSG
			}
		case "aws_security_group_rule", "aws_vpc_security_group_ingress_rule", "aws_vpc_security_group_egress_rule":
			group := r.Observed.Attributes["security_group_id"]
			if defaults.securityGroups[group] {
				reason = reasonDefaultSG
			} else if eksGroups[group] {
				reason = reasonEKSClusterSG
			}
		}
		if reason != "" {
			excluded[r.Address.Type+" "+id] = reason
		}
	}
	return excluded, err
}

// defaultNetwork is what AWS creates on its own: default VPCs, their
// subnets, main route tables and internet gateways, and the default
// security group of every VPC.
type defaultNetwork struct {
	vpcs, parts, securityGroups map[string]bool
}

// config returns the AWS configuration the provider's services list with.
func (p *Provider) config(ctx context.Context) (aws.Config, error) {
	service := &Service{}
	service.Configure(harvest.ListerConfig{Settings: p.serviceArgs()})
	service.SetContext(ctx)
	return service.sdkConfig()
}

func (p *Provider) defaultNetwork(ctx context.Context, config aws.Config) (defaultNetwork, error) {
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
