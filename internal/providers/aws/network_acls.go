// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// NaclLister lists the network ACLs of a region, each by its ID.
//
// A VPC's default ACL is recorded as aws_default_network_acl, which the
// catalogue then skips, since AWS creates one with every VPC; every other
// ACL is recorded as aws_network_acl.
type NaclLister struct {
	Service
}

// List lists every network ACL, page by page. An API error is
// returned and ends the listing.
func (g *NaclLister) List() error {
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
		for _, acl := range page.NetworkAcls {
			resourceType := "aws_network_acl"
			if aws.ToBool(acl.IsDefault) {
				resourceType = "aws_default_network_acl"
			}
			id := aws.ToString(acl.NetworkAclId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, resourceType, "aws"))
		}
	}
	return nil
}
