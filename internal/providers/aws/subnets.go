// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// SubnetLister lists the subnets of a region as aws_subnet, each by its
// ID.
//
// Default subnets are listed too; ExcludedByDefault leaves them out of the
// selection later, together with the default VPC they belong to.
type SubnetLister struct {
	Service
}

// List lists every subnet, page by page. An API error is returned
// and ends the listing.
func (g *SubnetLister) List() error {
	cfg, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(cfg)

	pages := ec2.NewDescribeSubnetsPaginator(client, &ec2.DescribeSubnetsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, subnet := range page.Subnets {
			id := aws.ToString(subnet.SubnetId)
			g.Resources = append(g.Resources, harvest.Record(id, id, "aws_subnet", "aws", ownerAttributes(subnet.OwnerId)))
		}
	}
	return nil
}
