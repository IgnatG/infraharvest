// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"
)

// The lister lists NAT gateways by ID, across pages, leaving out those
// deleted, being deleted or failed, which AWS lists for about an hour.
func TestRecordNatGatewayListsLiveGateways(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeNatGateways" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return recordEC2(call.Op, `<natGatewaySet>
				<item><natGatewayId>nat-failed</natGatewayId><state>failed</state></item>
				<item><natGatewayId>nat-pending</natGatewayId><state>pending</state></item>
				<item><natGatewayId>nat-deleting</natGatewayId><state>deleting</state></item>
				</natGatewaySet><nextToken></nextToken>`)
		}
		return recordEC2(call.Op, fmt.Sprintf(`<natGatewaySet>
			<item><natGatewayId>nat-a</natGatewayId><subnetId>subnet-a</subnetId><vpcId>vpc-1</vpcId><state>available</state><connectivityType>public</connectivityType></item>
			<item><natGatewayId>nat-gone</natGatewayId><state>deleted</state></item>
			</natGatewaySet><nextToken>%s</nextToken>`, nextPageToken))
	})
	g := &NatGatewayLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_nat_gateway", "nat-a", "nat-a", nil},
		recordedResource{"aws_nat_gateway", "nat-pending", "nat-pending", nil},
	)
}

func TestRecordNatGatewayReturnsErrors(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return recordEC2Error()
	})
	g := &NatGatewayLister{}

	assertRecordedError(t, g.List(), "UnauthorizedOperation")
}
