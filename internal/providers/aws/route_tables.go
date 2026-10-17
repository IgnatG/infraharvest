// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"slices"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// RouteTableLister lists the route tables of a region as aws_route_table,
// each by its ID (named as routeTableName says), recording its VPC as
// vpc_id, main "true" for a VPC's main route table and as_created "true"
// for one as AWS creates it (see asCreated), and followed by its
// associations:
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
		harvest.RecordBare(tableID, routeTableName(table), "aws_route_table", "aws"),
	}
	resources[0].Observed.Attributes["vpc_id"] = aws.ToString(table.VpcId)
	if slices.ContainsFunc(table.Associations, func(a types.RouteTableAssociation) bool { return aws.ToBool(a.Main) }) {
		resources[0].Observed.Attributes["main"] = "true"
		if asCreated(table) {
			resources[0].Observed.Attributes["as_created"] = "true"
		}
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

// routeTableName is what a route table is named after, by ID, which the
// import names after that resource's Name tag (a Name tag of the table's
// own comes first): its VPC, followed by main, for a VPC's main route
// table; the subnet, for a table only one subnet uses; else the table's
// own ID.
func routeTableName(table *types.RouteTable) string {
	var subnets []string
	for _, association := range table.Associations {
		if aws.ToBool(association.Main) {
			return aws.ToString(table.VpcId) + " main"
		}
		if subnet := aws.ToString(association.SubnetId); subnet != "" {
			subnets = append(subnets, subnet)
		}
	}
	if len(subnets) == 1 {
		return subnets[0]
	}
	return aws.ToString(table.RouteTableId)
}

// asCreated reports whether a main route table is as AWS creates it with
// its VPC: without tags, routes but the VPC's local ones, propagated
// routes, or associations but the main one.
func asCreated(table *types.RouteTable) bool {
	if len(table.Tags) > 0 || len(table.PropagatingVgws) > 0 {
		return false
	}
	for _, route := range table.Routes {
		if aws.ToString(route.GatewayId) != "local" {
			return false
		}
	}
	for _, association := range table.Associations {
		if !aws.ToBool(association.Main) {
			return false
		}
	}
	return true
}
