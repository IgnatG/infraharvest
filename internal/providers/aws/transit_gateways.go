// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// TransitGatewayLister lists the transit gateways of a region, their
// route tables and their VPC attachments, each by its ID:
//
//   - aws_ec2_transit_gateway for the gateways;
//   - aws_ec2_transit_gateway_route_table for their route tables, except
//     each gateway's default association route table, which AWS creates
//     with the gateway and the gateway's own settings describe;
//   - aws_ec2_transit_gateway_vpc_attachment for their VPC attachments.
//
// Those being deleted or deleted, and attachments that failed or were
// rejected, are left out: EC2 keeps reporting them for a while, and
// Terraform reads them as gone.
type TransitGatewayLister struct {
	Service
}

// List lists the gateways, then the route tables, then the VPC
// attachments, each page by page. An error in any of the three fails the
// service.
func (g *TransitGatewayLister) List() error {
	cfg, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(cfg)
	ctx := g.Context()

	lists := []struct {
		name string
		list func(context.Context, *ec2.Client) ([]harvest.Resource, error)
	}{
		{"transit gateways", listTransitGateways},
		{"transit gateway route tables", listTransitGatewayRouteTables},
		{"transit gateway VPC attachments", listTransitGatewayVpcAttachments},
	}
	for _, l := range lists {
		resources, err := l.list(ctx, client)
		if err != nil {
			return fmt.Errorf("listing %s: %w", l.name, err)
		}
		g.Resources = append(g.Resources, resources...)
	}
	return nil
}

// listTransitGateways returns every transit gateway as
// aws_ec2_transit_gateway.
func listTransitGateways(ctx context.Context, client *ec2.Client) ([]harvest.Resource, error) {
	var resources []harvest.Resource
	pages := ec2.NewDescribeTransitGatewaysPaginator(client, &ec2.DescribeTransitGatewaysInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, gateway := range page.TransitGateways {
			switch gateway.State {
			case types.TransitGatewayStateDeleting, types.TransitGatewayStateDeleted:
				continue
			}
			id := aws.ToString(gateway.TransitGatewayId)
			resources = append(resources, harvest.Record(id, id, "aws_ec2_transit_gateway", "aws", ownerAttributes(gateway.OwnerId)))
		}
	}
	return resources, nil
}

// listTransitGatewayRouteTables returns every transit gateway route table
// but the default association ones, as aws_ec2_transit_gateway_route_table.
// A table that does not say whether it is a default one is listed.
func listTransitGatewayRouteTables(ctx context.Context, client *ec2.Client) ([]harvest.Resource, error) {
	var resources []harvest.Resource
	pages := ec2.NewDescribeTransitGatewayRouteTablesPaginator(client, &ec2.DescribeTransitGatewayRouteTablesInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, table := range page.TransitGatewayRouteTables {
			switch table.State {
			case types.TransitGatewayRouteTableStateDeleting, types.TransitGatewayRouteTableStateDeleted:
				continue
			}
			if aws.ToBool(table.DefaultAssociationRouteTable) {
				continue
			}
			id := aws.ToString(table.TransitGatewayRouteTableId)
			resources = append(resources, harvest.RecordBare(id, id, "aws_ec2_transit_gateway_route_table", "aws"))
		}
	}
	return resources, nil
}

// listTransitGatewayVpcAttachments returns every transit gateway VPC
// attachment as aws_ec2_transit_gateway_vpc_attachment.
func listTransitGatewayVpcAttachments(ctx context.Context, client *ec2.Client) ([]harvest.Resource, error) {
	var resources []harvest.Resource
	pages := ec2.NewDescribeTransitGatewayVpcAttachmentsPaginator(client, &ec2.DescribeTransitGatewayVpcAttachmentsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, attachment := range page.TransitGatewayVpcAttachments {
			switch attachment.State {
			case types.TransitGatewayAttachmentStateDeleting, types.TransitGatewayAttachmentStateDeleted,
				types.TransitGatewayAttachmentStateFailing, types.TransitGatewayAttachmentStateFailed,
				types.TransitGatewayAttachmentStateRejecting, types.TransitGatewayAttachmentStateRejected:
				continue
			}
			id := aws.ToString(attachment.TransitGatewayAttachmentId)
			resources = append(resources, harvest.RecordBare(id, id, "aws_ec2_transit_gateway_vpc_attachment", "aws"))
		}
	}
	return resources, nil
}
