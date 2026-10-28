// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
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
