// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// VpcEndpointLister lists the VPC endpoints of a region as
// aws_vpc_endpoint, each by its ID. Endpoints of every type (gateway,
// interface, load balancer) and in every state are listed.
type VpcEndpointLister struct {
	Service
}

// List lists every VPC endpoint, page by page. An API error is
// returned and ends the listing.
func (g *VpcEndpointLister) List() error {
	cfg, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(cfg)

	pages := ec2.NewDescribeVpcEndpointsPaginator(client, &ec2.DescribeVpcEndpointsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, endpoint := range page.VpcEndpoints {
			id := aws.ToString(endpoint.VpcEndpointId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_vpc_endpoint", "aws"))
		}
	}
	return nil
}
