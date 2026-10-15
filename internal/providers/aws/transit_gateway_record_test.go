// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

// recordTransitGatewayAPI answers the transit gateway lister with two pages
// of each list, failing the operations in failing.
func recordTransitGatewayAPI(t *testing.T, failing ...string) func(apiCall) string {
	return func(call apiCall) string {
		for _, op := range failing {
			if call.Op == op {
				return recordEC2Error()
			}
		}
		second := recordSecondPage(call)
		next := `<nextToken>` + nextPageToken + `</nextToken>`
		switch call.Op {
		case "DescribeTransitGateways":
			if second {
				return recordEC2(call.Op, `<transitGatewaySet><item><transitGatewayId>tgw-2</transitGatewayId><state>pending</state></item>
					<item><transitGatewayId>tgw-old</transitGatewayId><state>deleted</state></item></transitGatewaySet>`)
			}
			return recordEC2(call.Op, `<transitGatewaySet><item><transitGatewayId>tgw-1</transitGatewayId><state>available</state><ownerId>123456789012</ownerId></item></transitGatewaySet>`+next)
		case "DescribeTransitGatewayRouteTables":
			if second {
				return recordEC2(call.Op, `<transitGatewayRouteTables>
					<item><transitGatewayRouteTableId>tgw-rtb-2</transitGatewayRouteTableId><transitGatewayId>tgw-2</transitGatewayId><defaultAssociationRouteTable>false</defaultAssociationRouteTable><defaultPropagationRouteTable>true</defaultPropagationRouteTable></item>
					<item><transitGatewayRouteTableId>tgw-rtb-old</transitGatewayRouteTableId><transitGatewayId>tgw-old</transitGatewayId><state>deleting</state><defaultAssociationRouteTable>false</defaultAssociationRouteTable></item>
					</transitGatewayRouteTables>`)
			}
			return recordEC2(call.Op, `<transitGatewayRouteTables>
				<item><transitGatewayRouteTableId>tgw-rtb-default</transitGatewayRouteTableId><transitGatewayId>tgw-1</transitGatewayId><defaultAssociationRouteTable>true</defaultAssociationRouteTable><defaultPropagationRouteTable>true</defaultPropagationRouteTable></item>
				<item><transitGatewayRouteTableId>tgw-rtb-1</transitGatewayRouteTableId><transitGatewayId>tgw-1</transitGatewayId><defaultAssociationRouteTable>false</defaultAssociationRouteTable><defaultPropagationRouteTable>false</defaultPropagationRouteTable></item>
				</transitGatewayRouteTables>`+next)
		case "DescribeTransitGatewayVpcAttachments":
			if second {
				return recordEC2(call.Op, `<transitGatewayVpcAttachments><item><transitGatewayAttachmentId>tgw-attach-2</transitGatewayAttachmentId><transitGatewayId>tgw-2</transitGatewayId><vpcId>vpc-2</vpcId></item>
					<item><transitGatewayAttachmentId>tgw-attach-old</transitGatewayAttachmentId><transitGatewayId>tgw-2</transitGatewayId><vpcId>vpc-3</vpcId><state>rejected</state></item></transitGatewayVpcAttachments>`)
			}
			return recordEC2(call.Op, `<transitGatewayVpcAttachments><item><transitGatewayAttachmentId>tgw-attach-1</transitGatewayAttachmentId><transitGatewayId>tgw-1</transitGatewayId><vpcId>vpc-1</vpcId><state>available</state></item></transitGatewayVpcAttachments>`+next)
		}
		return recordUnexpected(t, call)
	}
}

// The lister lists transit gateways, their route tables but the default
// association ones, which AWS creates with a gateway, and their VPC
// attachments, each by ID and across pages, leaving out those deleted or
// rejected.
func TestRecordTransitGatewayListsGatewaysRouteTablesAndAttachments(t *testing.T) {
	useFakeAPI(t, recordTransitGatewayAPI(t))
	g := &TransitGatewayLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_ec2_transit_gateway", "tgw-1", "tgw-1", nil},
		recordedResource{"aws_ec2_transit_gateway", "tgw-2", "tgw-2", nil},
		recordedResource{"aws_ec2_transit_gateway_route_table", "tgw-rtb-1", "tgw-rtb-1", nil},
		recordedResource{"aws_ec2_transit_gateway_route_table", "tgw-rtb-2", "tgw-rtb-2", nil},
		recordedResource{"aws_ec2_transit_gateway_vpc_attachment", "tgw-attach-1", "tgw-attach-1", nil},
		recordedResource{"aws_ec2_transit_gateway_vpc_attachment", "tgw-attach-2", "tgw-attach-2", nil},
	)
}

// Each list fails the service.
func TestRecordTransitGatewayReturnsErrors(t *testing.T) {
	for _, failing := range []string{"DescribeTransitGateways", "DescribeTransitGatewayRouteTables", "DescribeTransitGatewayVpcAttachments"} {
		t.Run(failing, func(t *testing.T) {
			useFakeAPI(t, recordTransitGatewayAPI(t, failing))
			g := &TransitGatewayLister{}

			assertRecordedError(t, g.List(), "UnauthorizedOperation")
		})
	}
}
