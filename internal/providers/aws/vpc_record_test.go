// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"
)

// The lister lists every VPC by ID, across pages, the default VPC included
// (ExcludedByDefault leaves it out later).
func TestRecordVpcListsVpcs(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeVpcs" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return recordEC2(call.Op, `<vpcSet>
				<item><vpcId>vpc-default</vpcId><cidrBlock>172.31.0.0/16</cidrBlock><isDefault>true</isDefault><state>available</state></item>
				</vpcSet><nextToken></nextToken>`)
		}
		return recordEC2(call.Op, fmt.Sprintf(`<vpcSet>
			<item><vpcId>vpc-1</vpcId><cidrBlock>10.0.0.0/16</cidrBlock><isDefault>false</isDefault><state>available</state>
				<tagSet><item><key>Name</key><value>main</value></item></tagSet></item>
			</vpcSet><nextToken>%s</nextToken>`, nextPageToken))
	})
	g := &VpcLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_vpc", "vpc-1", "vpc-1", map[string]string{"is_default": "false"}},
		recordedResource{"aws_vpc", "vpc-default", "vpc-default", map[string]string{"is_default": "true"}},
	)
}

func TestRecordVpcReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordEC2Error()
	})
	g := &VpcLister{}
	g.SetContext(ctx)

	assertRecordedError(t, g.List(), "UnauthorizedOperation")
}
