// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// IgwLister lists the internet gateways of a region as
// aws_internet_gateway, each by its ID.
//
// Only gateways attached to a VPC are listed, whatever the state of the
// attachment; a gateway with no attachment at all is left out. The gateway
// attached to the default VPC is listed too, and ExcludedByDefault leaves it
// out of the selection later. Each records the VPC it is attached to, the
// first if there are more, as vpc_id.
type IgwLister struct {
	Service
}

// List lists the attached internet gateways, page by page. An API
// error is returned and ends the listing.
func (g *IgwLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *IgwLister) list() error {
	cfg, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(cfg)

	pages := ec2.NewDescribeInternetGatewaysPaginator(client, &ec2.DescribeInternetGatewaysInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, gateway := range page.InternetGateways {
			if len(gateway.Attachments) == 0 {
				continue
			}
			id := aws.ToString(gateway.InternetGatewayId)
			g.Resources = append(g.Resources, harvest.Record(id, id, "aws_internet_gateway", "aws", map[string]string{
				"vpc_id": aws.ToString(gateway.Attachments[0].VpcId),
			}))
		}
	}
	return nil
}
