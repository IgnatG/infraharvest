// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"
)

const recordRouteTablesPage1 = `<routeTableSet>
	<item><routeTableId>rtb-main</routeTableId><vpcId>vpc-1</vpcId><ownerId>123456789012</ownerId>
		<associationSet>
			<item><main>true</main><routeTableAssociationId>rtbassoc-main</routeTableAssociationId><routeTableId>rtb-main</routeTableId><associationState><state>associated</state></associationState></item>
			<item><main>false</main><routeTableAssociationId>rtbassoc-a</routeTableAssociationId><routeTableId>rtb-main</routeTableId><subnetId>subnet-a</subnetId><associationState><state>associated</state></associationState></item>
		</associationSet>
		<routeSet><item><destinationCidrBlock>10.0.0.0/16</destinationCidrBlock><gatewayId>local</gatewayId><state>active</state><origin>CreateRouteTable</origin></item></routeSet></item>
	</routeTableSet>`

// The lister lists each route table by ID, across pages, followed by its
// associations, each by its association ID: the main one as
// aws_main_route_table_association named after the VPC, a subnet's or a
// gateway's as aws_route_table_association named after the subnet or
// gateway. An association with none of these is left out.
func TestRecordRouteTableListsTablesAndAssociations(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeRouteTables" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return recordEC2(call.Op, `<routeTableSet>
				<item><routeTableId>rtb-edge</routeTableId><vpcId>vpc-1</vpcId>
					<associationSet>
						<item><main>false</main><routeTableAssociationId>rtbassoc-igw</routeTableAssociationId><routeTableId>rtb-edge</routeTableId><gatewayId>igw-1</gatewayId></item>
						<item><main>false</main><routeTableAssociationId>rtbassoc-odd</routeTableAssociationId><routeTableId>rtb-edge</routeTableId></item>
					</associationSet></item>
				<item><routeTableId>rtb-lonely</routeTableId><vpcId>vpc-2</vpcId><associationSet/></item>
				</routeTableSet><nextToken></nextToken>`)
		}
		return recordEC2(call.Op, fmt.Sprintf(`%s<nextToken>%s</nextToken>`, recordRouteTablesPage1, nextPageToken))
	})
	g := &RouteTableLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_route_table", "rtb-main", "rtb-main", nil},
		recordedResource{"aws_main_route_table_association", "rtbassoc-main", "vpc-1", map[string]string{
			"vpc_id": "vpc-1", "route_table_id": "rtb-main"}},
		recordedResource{"aws_route_table_association", "rtbassoc-a", "subnet-a", map[string]string{
			"subnet_id": "subnet-a", "route_table_id": "rtb-main"}},
		recordedResource{"aws_route_table", "rtb-edge", "rtb-edge", nil},
		recordedResource{"aws_route_table_association", "rtbassoc-igw", "igw-1", map[string]string{
			"gateway_id": "igw-1", "route_table_id": "rtb-edge"}},
		recordedResource{"aws_route_table", "rtb-lonely", "rtb-lonely", nil},
	)
}

// An error on any page fails the service.
func TestRecordRouteTableReturnsErrors(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if recordSecondPage(call) {
			return recordEC2Error()
		}
		return recordEC2(call.Op, fmt.Sprintf(`%s<nextToken>%s</nextToken>`, recordRouteTablesPage1, nextPageToken))
	})
	g := &RouteTableLister{}

	assertRecordedError(t, g.List(), "UnauthorizedOperation")
}
