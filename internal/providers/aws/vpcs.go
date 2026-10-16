// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// VpcLister lists the VPCs of a region as aws_vpc, each by its ID.
//
// The default VPC is listed like any other; ExcludedByDefault leaves it out
// of the selection later, because AWS creates one in every region.
type VpcLister struct {
	Service
}

// List lists every VPC, page by page. An API error is returned and
// ends the listing.
func (g *VpcLister) List() error {
	cfg, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(cfg)

	pages := ec2.NewDescribeVpcsPaginator(client, &ec2.DescribeVpcsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, vpc := range page.Vpcs {
			id := aws.ToString(vpc.VpcId)
			g.Resources = append(g.Resources, harvest.Record(id, id, "aws_vpc", "aws", ownerAttributes(vpc.OwnerId)))
		}
	}
	return nil
}
