// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// SubnetLister lists the subnets of a region as aws_subnet, each by its
// ID.
//
// Each records the VPC it is in as vpc_id. Default subnets are listed too;
// ExcludedByDefault leaves them out of the selection later, together with
// the default VPC they belong to.
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
			g.Resources = append(g.Resources, harvest.Record(id, id, "aws_subnet", "aws", subnetAttributes(subnet.OwnerId, subnet.VpcId)))
		}
	}
	return nil
}

// subnetAttributes are the attributes a subnet records: its owner, as
// ownerAttributes gives, and the VPC it is in, as vpc_id.
func subnetAttributes(owner, vpc *string) map[string]string {
	attributes := ownerAttributes(owner)
	attributes["vpc_id"] = aws.ToString(vpc)
	return attributes
}
