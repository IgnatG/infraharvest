// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister lists every subnet by ID, across pages, default subnets
// included (ExcludedByDefault leaves those out later).
func TestRecordSubnetListsSubnets(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeSubnets" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return recordEC2(call.Op, `<subnetSet>
				<item><subnetId>subnet-default-a</subnetId><vpcId>vpc-default</vpcId><cidrBlock>172.31.0.0/20</cidrBlock><defaultForAz>true</defaultForAz><availabilityZone>us-east-1a</availabilityZone><state>available</state></item>
				</subnetSet><nextToken></nextToken>`)
		}
		return recordEC2(call.Op, fmt.Sprintf(`<subnetSet>
			<item><subnetId>subnet-a</subnetId><vpcId>vpc-1</vpcId><cidrBlock>10.0.1.0/24</cidrBlock><defaultForAz>false</defaultForAz><availabilityZone>us-east-1a</availabilityZone><state>available</state></item>
			<item><subnetId>subnet-b</subnetId><vpcId>vpc-1</vpcId><cidrBlock>10.0.2.0/24</cidrBlock><state>pending</state></item>
			</subnetSet><nextToken>%s</nextToken>`, nextPageToken))
	})
	g := &SubnetLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_subnet", "subnet-a", "subnet-a", map[string]string{"vpc_id": "vpc-1"}},
		recordedResource{"aws_subnet", "subnet-b", "subnet-b", map[string]string{"vpc_id": "vpc-1"}},
		recordedResource{"aws_subnet", "subnet-default-a", "subnet-default-a", map[string]string{"vpc_id": "vpc-default"}},
	)
}

func TestRecordSubnetReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordEC2Error()
	})
	g := &SubnetLister{}

	assertRecordedError(t, listError(ctx, g), "UnauthorizedOperation")
}
