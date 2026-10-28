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

// NatGatewayLister lists the NAT gateways of a region as aws_nat_gateway,
// each by its ID.
type NatGatewayLister struct {
	Service
}

// goneNatGatewayStates are the states of NAT gateways that are gone or
// going. AWS goes on listing such gateways for about an hour, but there is
// nothing left to import.
var goneNatGatewayStates = map[types.NatGatewayState]bool{
	types.NatGatewayStateDeleted:  true,
	types.NatGatewayStateDeleting: true,
	types.NatGatewayStateFailed:   true,
}

// List lists the NAT gateways that are not gone, page by page. An
// API error is returned and ends the listing.
func (g *NatGatewayLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *NatGatewayLister) list() error {
	cfg, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(cfg)

	pages := ec2.NewDescribeNatGatewaysPaginator(client, &ec2.DescribeNatGatewaysInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, gateway := range page.NatGateways {
			if goneNatGatewayStates[gateway.State] {
				continue
			}
			id := aws.ToString(gateway.NatGatewayId)
			g.Resources = append(g.Resources, harvest.RecordBare(id, id, "aws_nat_gateway", "aws"))
		}
	}
	return nil
}
