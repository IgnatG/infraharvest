// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// NaclLister lists the network ACLs of a region, each by its ID.
//
// A VPC's default ACL is recorded as aws_default_network_acl, with
// as_created "true" while it is as AWS creates it with the VPC (see
// defaultACLAsCreated), which the catalogue then skips; every other ACL is
// recorded as aws_network_acl.
type NaclLister struct {
	Service
}

// List lists every network ACL, page by page. An API error is
// returned and ends the listing.
func (g *NaclLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *NaclLister) list() error {
	cfg, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(cfg)

	pages := ec2.NewDescribeNetworkAclsPaginator(client, &ec2.DescribeNetworkAclsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for i := range page.NetworkAcls {
			acl := &page.NetworkAcls[i]
			id := aws.ToString(acl.NetworkAclId)
			if !aws.ToBool(acl.IsDefault) {
				g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_network_acl", "aws"))
				continue
			}
			resource := harvest.RecordBare(id, id, "aws_default_network_acl", "aws")
			if defaultACLAsCreated(acl) {
				resource.Observed.Attributes["as_created"] = "true"
			}
			g.Resources = append(g.Resources, resource)
		}
	}
	return nil
}

// defaultACLAsCreated reports whether a VPC's default network ACL is as AWS
// creates it: without tags, and with no entries but the ones AWS gives it,
// rule 100 allowing all IPv4 traffic and, in a VPC with IPv6, rule 101
// allowing all IPv6 traffic, each way, besides the catch-all deny rules
// nothing can change. Configuration that adopts the default ACL, as the
// terraform-aws-vpc module does by default, tags it or changes its rules.
func defaultACLAsCreated(acl *types.NetworkAcl) bool {
	if len(acl.Tags) > 0 {
		return false
	}
	for _, entry := range acl.Entries {
		rule := aws.ToInt32(entry.RuleNumber)
		if rule >= 32767 {
			continue
		}
		allowsAll := rule == 100 && aws.ToString(entry.CidrBlock) == "0.0.0.0/0" && entry.Ipv6CidrBlock == nil ||
			rule == 101 && aws.ToString(entry.Ipv6CidrBlock) == "::/0" && entry.CidrBlock == nil
		if !allowsAll || entry.RuleAction != types.RuleActionAllow || aws.ToString(entry.Protocol) != "-1" {
			return false
		}
	}
	return true
}
