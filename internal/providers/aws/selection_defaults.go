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
// their rules, the KMS grants AWS services make for themselves, and the
// Auto Scaling groups and launch templates of EKS managed node groups,
// which AWS creates itself; the replicas of a DynamoDB global table in
// every region but the one that imports the table with its replicas; what
// CloudFormation stacks manage, which would otherwise have two owners,
// matched by physical ID and, where cloudFormationTypes knows it, by type;
// what other accounts share; and the targets Auto Scaling groups and ECS
// services register in their target groups, which they register and
// deregister themselves. The catalogue's skip rules leave out what IDs
// tell, such as service-linked roles.
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
	cat := p.catalogue()
	types := stackTypes(cat)
	for _, r := range resources {
		key := r.Address.Type + " " + r.Observed.ID
		stack, ok := managingStack(stacks[r.Observed.ID], cat, types, r.Address.Type)
		if !ok {
			if id, importable := importID(cat, r); importable && id != r.Observed.ID {
				stack, ok = managingStack(stacks[id], cat, types, r.Address.Type)
			}
		}
		if ok {
			excluded[key] = reasonCloudFormation(stack)
		}
	}
	excludeServiceGrants(resources, excluded)
	excludeEKSNodeGroups(resources, excluded)
	excludeGlobalTableReplicas(resources, config.Region, excluded)
	sharedErr := p.excludeShared(ctx, config, resources, excluded)
	targetsErr := p.excludeRegisteredTargets(ctx, config, resources, excluded)
	stacksErr = errors.Join(stacksErr, sharedErr, targetsErr)
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

// sharedTypes are the types another account can share with this one
// through AWS RAM, whose listers record the owner_id.
var sharedTypes = map[string]bool{"aws_vpc": true, "aws_subnet": true, "aws_ec2_transit_gateway": true}

// reasonShared is why the default selection leaves out what another
// account shares with this one: that account manages it, and this one
// can't change it; the configuration reads it through a data source.
func reasonShared(owner string) string {
	return "shared by account " + owner + " through AWS RAM, which manages it"
}

// excludeShared adds to excluded the VPCs, subnets and transit gateways of
// resources that another account owns and shares with this one. It asks
// for the caller's account only if one of them records an owner.
func (p *Provider) excludeShared(ctx context.Context, config aws.Config, resources []harvest.Resource, excluded map[string]string) error {
	var owned []harvest.Resource
	for _, r := range resources {
		if sharedTypes[r.Address.Type] && r.Observed.Attributes["owner_id"] != "" {
			owned = append(owned, r)
		}
	}
	if len(owned) == 0 {
		return nil
	}
	service := &Service{}
	service.SetContext(ctx)
	account, err := service.callerAccount(config)
	if err != nil {
		return fmt.Errorf("find the AWS account, for what other accounts share: %w", err)
	}
	for _, r := range owned {
		if owner := r.Observed.Attributes["owner_id"]; owner != aws.ToString(account) {
			excluded[r.Address.Type+" "+r.Observed.ID] = reasonShared(owner)
		}
	}
	return nil
}

// eksNodeGroupTag is the tag EKS gives the Auto Scaling group and the
// launch template it creates for a managed node group, naming the group.
const eksNodeGroupTag = "tags.eks:nodegroup-name"

// reasonEKSNodeGroup is why the default selection leaves out the Auto
// Scaling group and the launch template EKS creates for a managed node
// group: the aws_eks_node_group manages them, and Terraform would fight EKS
// over the group's size and the template's versions.
func reasonEKSNodeGroup(nodeGroup string) string {
	return "created by EKS for managed node group " + nodeGroup + ", which manages it"
}

// excludeEKSNodeGroups adds to excluded the Auto Scaling groups and launch
// templates of resources that EKS created for a managed node group, told
// by the tags the auto_scaling lister records.
func excludeEKSNodeGroups(resources []harvest.Resource, excluded map[string]string) {
	for _, r := range resources {
		if r.Address.Type != "aws_autoscaling_group" && r.Address.Type != "aws_launch_template" {
			continue
		}
		if nodeGroup := r.Observed.Attributes[eksNodeGroupTag]; nodeGroup != "" {
			excluded[r.Address.Type+" "+r.Observed.ID] = reasonEKSNodeGroup(nodeGroup)
		}
	}
}

// reasonServiceGrant is why the default selection leaves out a KMS grant
// an AWS service makes for itself, as RDS and EBS do for the keys they
// encrypt with: the service creates and retires it, and as every argument
// of a grant forces a new one, replacing it could break decryption.
func reasonServiceGrant(grantee string) string {
	return "grant to " + grantee + ", which an AWS service makes for itself and manages"
}

// awsServiceGrantee reports whether a grant's grantee principal is an AWS
// service: a service principal, such as rds.eu-west-2.amazonaws.com, or a
// service-linked role, whose path is /aws-service-role/.
func awsServiceGrantee(grantee string) bool {
	return strings.HasSuffix(grantee, ".amazonaws.com") || strings.HasSuffix(grantee, ".amazonaws.com.cn") ||
		strings.Contains(grantee, ":role/aws-service-role/")
}

// excludeServiceGrants adds to excluded the KMS grants of resources whose
// grantee principal, which the kms lister records, is an AWS service.
func excludeServiceGrants(resources []harvest.Resource, excluded map[string]string) {
	for _, r := range resources {
		if r.Address.Type != "aws_kms_grant" {
			continue
		}
		if grantee := r.Observed.Attributes["grantee_principal"]; awsServiceGrantee(grantee) {
			excluded[r.Address.Type+" "+r.Observed.ID] = reasonServiceGrant(grantee)
		}
	}
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

// ownerAttributes are the attributes of a resource another account may
// own (see sharedTypes): its owner_id, when AWS gives one, in a map of the
// resource's own, as RecordBare gives.
func ownerAttributes(owner *string) map[string]string {
	attributes := map[string]string{}
	if id := aws.ToString(owner); id != "" {
		attributes["owner_id"] = id
	}
	return attributes
}

// reasonGlobalTableReplica is why the default selection leaves out a
// DynamoDB table that is a replica of a global table imported in another
// region: there the aws_dynamodb_table manages the replica through a
// replica block, and importing it here too would give it two owners.
func reasonGlobalTableReplica(home string) string {
	return "a replica of the global table in " + home
}

// excludeGlobalTableReplicas adds to excluded the DynamoDB tables of
// resources, listed in region, that are replicas of a global table whose
// home is another region (see globalTableHome).
func excludeGlobalTableReplicas(resources []harvest.Resource, region string, excluded map[string]string) {
	for _, r := range resources {
		if r.Address.Type != "aws_dynamodb_table" {
			continue
		}
		if home := globalTableHome(r, region); home != region {
			excluded[r.Address.Type+" "+r.Observed.ID] = reasonGlobalTableReplica(home)
		}
	}
}
