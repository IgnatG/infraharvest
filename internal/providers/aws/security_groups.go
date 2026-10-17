// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// SecurityLister lists a region's VPC security groups and their rules.
// Each rule is a resource of its own, an aws_vpc_security_group_ingress_rule
// or aws_vpc_security_group_egress_rule, rather than an inline block of
// its group: Terraform can then manage a group's rules one by one, and the
// default selection can leave out the rules of the groups AWS creates.
type SecurityLister struct {
	Service
}

// List reads every security group, then every rule, and records
// them. Errors are returned.
func (g *SecurityLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := ec2.NewFromConfig(config)

	var groups []ec2types.SecurityGroup
	groupPages := ec2.NewDescribeSecurityGroupsPaginator(svc, &ec2.DescribeSecurityGroupsInput{}, stopOnDuplicateToken)
	for groupPages.HasMorePages() {
		page, err := groupPages.NextPage(g.Context())
		if err != nil {
			return err
		}
		groups = append(groups, page.SecurityGroups...)
	}

	var rules []ec2types.SecurityGroupRule
	rulePages := ec2.NewDescribeSecurityGroupRulesPaginator(svc, &ec2.DescribeSecurityGroupRulesInput{}, stopOnDuplicateToken)
	for rulePages.HasMorePages() {
		page, err := rulePages.NextPage(g.Context())
		if err != nil {
			return err
		}
		rules = append(rules, page.SecurityGroupRules...)
	}

	g.Resources = g.createResources(groups, rules)
	return nil
}

// createResources returns the groups, in the order given, followed by the
// rules of those groups, in the order given.
//
// A group without a VPC is an EC2-Classic one, which the VPC types can't
// manage, so it is left out with its rules. Groups and rules record their
// tags as tags.<key> attributes, which the default selection reads (see
// eksClusterTag), groups their name as group_name, by which it tells a
// VPC's default group, and rules record the group they belong to as
// security_group_id, by which the default selection leaves out the rules
// of the groups it leaves out.
func (g SecurityLister) createResources(groups []ec2types.SecurityGroup, rules []ec2types.SecurityGroupRule) []harvest.Resource {
	var resources []harvest.Resource
	groupNames := map[string]string{}
	for _, group := range groups {
		if group.VpcId == nil {
			continue
		}
		id := aws.ToString(group.GroupId)
		name := aws.ToString(group.GroupName)
		groupNames[id] = name
		attributes := tagAttributes(group.Tags)
		attributes["group_name"] = name
		resources = append(resources, harvest.Record(id, strings.TrimSpace(name), "aws_security_group", "aws", attributes))
	}
	for _, rule := range rules {
		groupID := aws.ToString(rule.GroupId)
		groupName, ok := groupNames[groupID]
		if !ok || rule.SecurityGroupRuleId == nil {
			continue
		}
		resourceType, direction := "aws_vpc_security_group_ingress_rule", "ingress"
		if aws.ToBool(rule.IsEgress) {
			resourceType, direction = "aws_vpc_security_group_egress_rule", "egress"
		}
		attributes := tagAttributes(rule.Tags)
		attributes["security_group_id"] = groupID
		resources = append(resources, harvest.Record(aws.ToString(rule.SecurityGroupRuleId), ruleName(groupName, direction, rule), resourceType, "aws", attributes))
	}
	return resources
}

// tagAttributes returns tags as tags.<key> attributes, in a map of its own.
func tagAttributes(tags []ec2types.Tag) map[string]string {
	attributes := map[string]string{}
	for _, tag := range tags {
		attributes["tags."+aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return attributes
}

// ruleName names a rule after what it allows, since rule IDs say nothing:
// its group's name, its direction, its protocol and ports, and its peer,
// joined with "_", as in web_ingress_tcp_443_anywhere. Protocol "-1" is
// every protocol, written "all", whose ports mean nothing. Ports are
// written when both are set: one port, or "<from>_<to>" for a range. The
// peer is "anywhere" or "anywhere_ipv6" for every address, else the CIDR
// block, prefix list or security group the rule names, and left out if
// there is none.
func ruleName(group, direction string, rule ec2types.SecurityGroupRule) string {
	parts := []string{group, direction}
	if protocol := aws.ToString(rule.IpProtocol); protocol == "-1" {
		parts = append(parts, "all")
	} else {
		parts = append(parts, protocol)
		if rule.FromPort != nil && rule.ToPort != nil {
			from, to := *rule.FromPort, *rule.ToPort
			parts = append(parts, strconv.Itoa(int(from)))
			if from != to {
				parts = append(parts, strconv.Itoa(int(to)))
			}
		}
	}
	switch {
	case aws.ToString(rule.CidrIpv4) == "0.0.0.0/0":
		parts = append(parts, "anywhere")
	case aws.ToString(rule.CidrIpv6) == "::/0":
		parts = append(parts, "anywhere_ipv6")
	case rule.CidrIpv4 != nil:
		parts = append(parts, *rule.CidrIpv4)
	case rule.CidrIpv6 != nil:
		parts = append(parts, *rule.CidrIpv6)
	case rule.PrefixListId != nil:
		parts = append(parts, *rule.PrefixListId)
	case rule.ReferencedGroupInfo != nil && rule.ReferencedGroupInfo.GroupId != nil:
		parts = append(parts, *rule.ReferencedGroupInfo.GroupId)
	}
	return strings.Join(parts, "_")
}
