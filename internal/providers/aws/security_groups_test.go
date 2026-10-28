// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Each rule of a VPC security group is a resource of its own, named after
// what it allows; an EC2-Classic group, which has no VPC, is left out with
// its rules.
func TestSecurityGroupRulesAreResources(t *testing.T) {
	t.Parallel()
	groups := []ec2types.SecurityGroup{
		{
			GroupId:   aws.String("sg-1"),
			GroupName: aws.String("web"),
			VpcId:     aws.String("vpc-1"),
			Tags:      []ec2types.Tag{{Key: aws.String("aws:eks:cluster-name"), Value: aws.String("apps")}},
		},
		{GroupId: aws.String("sg-2"), GroupName: aws.String("classic")},
	}
	rules := []ec2types.SecurityGroupRule{
		{
			SecurityGroupRuleId: aws.String("sgr-1"),
			GroupId:             aws.String("sg-1"),
			IsEgress:            aws.Bool(false),
			IpProtocol:          aws.String("tcp"),
			FromPort:            aws.Int32(443),
			ToPort:              aws.Int32(443),
			CidrIpv4:            aws.String("0.0.0.0/0"),
			Tags:                []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("https")}},
		},
		{
			SecurityGroupRuleId: aws.String("sgr-2"),
			GroupId:             aws.String("sg-1"),
			IsEgress:            aws.Bool(true),
			IpProtocol:          aws.String("-1"),
			FromPort:            aws.Int32(-1),
			ToPort:              aws.Int32(-1),
			CidrIpv4:            aws.String("0.0.0.0/0"),
		},
		{
			SecurityGroupRuleId: aws.String("sgr-3"),
			GroupId:             aws.String("sg-1"),
			IsEgress:            aws.Bool(false),
			IpProtocol:          aws.String("tcp"),
			FromPort:            aws.Int32(8000),
			ToPort:              aws.Int32(8080),
			ReferencedGroupInfo: &ec2types.ReferencedSecurityGroup{GroupId: aws.String("sg-9")},
		},
		{
			SecurityGroupRuleId: aws.String("sgr-4"),
			GroupId:             aws.String("sg-2"),
			IsEgress:            aws.Bool(false),
			IpProtocol:          aws.String("tcp"),
			FromPort:            aws.Int32(22),
			ToPort:              aws.Int32(22),
		},
	}

	resources := SecurityLister{}.createResources(groups, rules)

	want := []struct{ typ, id, name string }{
		{"aws_security_group", "sg-1", "web"},
		{"aws_vpc_security_group_ingress_rule", "sgr-1", "web_ingress_tcp_443_anywhere"},
		{"aws_vpc_security_group_egress_rule", "sgr-2", "web_egress_all_anywhere"},
		{"aws_vpc_security_group_ingress_rule", "sgr-3", "web_ingress_tcp_8000_8080_sg-9"},
	}
	if len(resources) != len(want) {
		t.Fatalf("got %d resources, want %d: %+v", len(resources), len(want), resources)
	}
	for i, w := range want {
		r := resources[i]
		if r.Address.Type != w.typ || r.Observed.ID != w.id || r.RawName != w.name {
			t.Errorf("resource %d: got (%s, %s, %s), want (%s, %s, %s)",
				i, r.Address.Type, r.Observed.ID, r.RawName, w.typ, w.id, w.name)
		}
	}
	if got := resources[0].Observed.Attributes[eksClusterTag]; got != "apps" {
		t.Errorf("sg-1 %s = %q, want apps", eksClusterTag, got)
	}
	if got := resources[0].Observed.Attributes["group_name"]; got != "web" {
		t.Errorf("sg-1 group_name = %q, want web", got)
	}
	ingress := resources[1].Observed.Attributes
	if ingress["tags.Name"] != "https" || ingress["security_group_id"] != "sg-1" {
		t.Errorf("sgr-1 attributes: got %v, want tags.Name https and security_group_id sg-1", ingress)
	}
}

// A VPC's default group is as AWS created it when it has no tags and no
// rules but the ones AWS gives it: all inbound traffic from itself, all
// outbound IPv4 traffic and, with IPv6, all outbound IPv6 traffic. Only
// then are it and its rules recorded as_created, which the catalogue skips
// by default. One that is tagged, or whose rules were changed, as the
// terraform-aws-vpc module's, is imported with its rules, and so is a
// group of another name with the same rules.
func TestDefaultSecurityGroupsAsCreated(t *testing.T) {
	t.Parallel()
	group := func(id, name string, tags ...ec2types.Tag) ec2types.SecurityGroup {
		return ec2types.SecurityGroup{GroupId: aws.String(id), GroupName: aws.String(name), VpcId: aws.String("vpc-1"), Tags: tags}
	}
	fromItself := func(id string) ec2types.SecurityGroupRule {
		return ec2types.SecurityGroupRule{
			SecurityGroupRuleId: aws.String(id + "-in"), GroupId: aws.String(id), IsEgress: aws.Bool(false),
			IpProtocol: aws.String("-1"), ReferencedGroupInfo: &ec2types.ReferencedSecurityGroup{GroupId: aws.String(id)},
		}
	}
	toAll := func(id, suffix, ipv4, ipv6 string) ec2types.SecurityGroupRule {
		rule := ec2types.SecurityGroupRule{SecurityGroupRuleId: aws.String(id + suffix), GroupId: aws.String(id), IsEgress: aws.Bool(true), IpProtocol: aws.String("-1")}
		if ipv4 != "" {
			rule.CidrIpv4 = aws.String(ipv4)
		}
		if ipv6 != "" {
			rule.CidrIpv6 = aws.String(ipv6)
		}
		return rule
	}
	described := fromItself("sg-described")
	described.Description = aws.String("cluster nodes")
	ssh := ec2types.SecurityGroupRule{
		SecurityGroupRuleId: aws.String("sg-ssh-22"), GroupId: aws.String("sg-ssh"), IsEgress: aws.Bool(false),
		IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22), CidrIpv4: aws.String("10.0.0.0/8"),
	}
	groups := []ec2types.SecurityGroup{
		group("sg-new", "default"),
		group("sg-new-ipv6", "default"),
		group("sg-tagged", "default", ec2types.Tag{Key: aws.String("Name"), Value: aws.String("main-default")}),
		group("sg-emptied", "default"),
		group("sg-ssh", "default"),
		group("sg-described", "default"),
		group("sg-other", "default"),
		group("sg-web", "web"),
	}
	rules := []ec2types.SecurityGroupRule{
		fromItself("sg-new"), toAll("sg-new", "-out", "0.0.0.0/0", ""),
		fromItself("sg-new-ipv6"), toAll("sg-new-ipv6", "-out", "0.0.0.0/0", ""), toAll("sg-new-ipv6", "-out6", "", "::/0"),
		fromItself("sg-tagged"), toAll("sg-tagged", "-out", "0.0.0.0/0", ""),
		fromItself("sg-ssh"), toAll("sg-ssh", "-out", "0.0.0.0/0", ""), ssh,
		described, toAll("sg-described", "-out", "0.0.0.0/0", ""),
		fromItself("sg-other"), toAll("sg-other", "-out", "10.0.0.0/8", ""),
		fromItself("sg-web"), toAll("sg-web", "-out", "0.0.0.0/0", ""),
	}

	resources := SecurityLister{}.createResources(groups, rules)

	cat := awsCatalogue()
	var asCreated, skipped []string
	for _, r := range resources {
		if r.Observed.Attributes["as_created"] == "true" {
			asCreated = append(asCreated, r.Observed.ID)
		}
		if reason := cat.Skipped(r.Address.Type, r.Observed.ID, r.Observed.Attributes); reason != "" {
			if !strings.Contains(reason, "default security group") {
				t.Errorf("%s %s skipped as %q, want the default security group's reason", r.Address.Type, r.Observed.ID, reason)
			}
			skipped = append(skipped, r.Observed.ID)
		}
	}
	want := []string{"sg-new", "sg-new-ipv6", "sg-new-in", "sg-new-out", "sg-new-ipv6-in", "sg-new-ipv6-out", "sg-new-ipv6-out6"}
	if !slices.Equal(asCreated, want) {
		t.Errorf("as_created: got %v, want %v", asCreated, want)
	}
	if !slices.Equal(skipped, want) {
		t.Errorf("skipped by the catalogue: got %v, want %v", skipped, want)
	}
}
