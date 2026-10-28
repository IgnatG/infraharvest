// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"
)

// The lister lists the internet gateways attached to something, whatever
// the attachment's state, across pages, by ID; it leaves out the unattached
// ones.
func TestRecordIgwListsAttachedGateways(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeInternetGateways" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return recordEC2(call.Op, `<internetGatewaySet>
				<item><internetGatewayId>igw-detaching</internetGatewayId><attachmentSet><item><vpcId>vpc-2</vpcId><state>detaching</state></item></attachmentSet></item>
				<item><internetGatewayId>igw-spare</internetGatewayId><attachmentSet/></item>
				</internetGatewaySet><nextToken></nextToken>`)
		}
		return recordEC2(call.Op, fmt.Sprintf(`<internetGatewaySet>
			<item><internetGatewayId>igw-main</internetGatewayId><ownerId>123456789012</ownerId>
				<attachmentSet><item><vpcId>vpc-1</vpcId><state>available</state></item></attachmentSet>
				<tagSet><item><key>Name</key><value>main</value></item></tagSet></item>
			<item><internetGatewayId>igw-unattached</internetGatewayId></item>
			</internetGatewaySet><nextToken>%s</nextToken>`, nextPageToken))
	})
	g := &IgwLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_internet_gateway", "igw-main", "igw-main", map[string]string{"vpc_id": "vpc-1"}},
		recordedResource{"aws_internet_gateway", "igw-detaching", "igw-detaching", map[string]string{"vpc_id": "vpc-2"}},
	)
}

func TestRecordIgwReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordEC2Error()
	})
	g := &IgwLister{}
	g.SetContext(ctx)

	assertRecordedError(t, g.List(), "UnauthorizedOperation")
}
