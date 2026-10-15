// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// RouteTableLister lists the route tables of a region as aws_route_table,
// each by its ID and followed by its associations:
//
//   - the association that makes a table its VPC's main route table, as
//     aws_main_route_table_association named after the VPC (the catalogue
//     marks it not importable; the VPC records its main route table);
//   - a subnet's or a gateway's association, as aws_route_table_association
//     named after the subnet or the gateway.
//
// Each association is recorded by its association ID, with the attributes
// the catalogue builds its import ID from. An association that is none of
// these is left out.
type RouteTableLister struct {
	Service
}

// List lists every route table and its associations, page by page.
// An API error fails the service.
func (g *RouteTableLister) List() error {
	cfg, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ec2.NewFromConfig(cfg)

	pages := ec2.NewDescribeRouteTablesPaginator(client, &ec2.DescribeRouteTablesInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for i := range page.RouteTables {
			g.Resources = append(g.Resources, routeTableResources(&page.RouteTables[i])...)
		}
	}
	return nil
}

// routeTableResources returns the resources of one route table: the table
// first, then its associations in the order AWS gives them.
func routeTableResources(table *types.RouteTable) []harvest.Resource {
	tableID := aws.ToString(table.RouteTableId)
	resources := []harvest.Resource{
		harvest.RecordBare(tableID, tableID, "aws_route_table", "aws"),
	}
	for _, association := range table.Associations {
		associationID := aws.ToString(association.RouteTableAssociationId)
		switch {
		case aws.ToBool(association.Main):
			vpcID := aws.ToString(table.VpcId)
			resources = append(resources, harvest.Record(associationID, vpcID,
				"aws_main_route_table_association", "aws", map[string]string{
					"vpc_id":         vpcID,
					"route_table_id": tableID,
				}))
		case aws.ToString(association.SubnetId) != "":
			subnetID := aws.ToString(association.SubnetId)
			resources = append(resources, harvest.Record(associationID, subnetID,
				"aws_route_table_association", "aws", map[string]string{
					"subnet_id":      subnetID,
					"route_table_id": tableID,
				}))
		case aws.ToString(association.GatewayId) != "":
			gatewayID := aws.ToString(association.GatewayId)
			resources = append(resources, harvest.Record(associationID, gatewayID,
				"aws_route_table_association", "aws", map[string]string{
					"gateway_id":     gatewayID,
					"route_table_id": tableID,
				}))
		}
	}
	return resources
}
