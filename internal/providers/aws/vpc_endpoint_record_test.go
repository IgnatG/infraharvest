// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

// TestVpcEndpointPaginates (pagination_test.go) pins the IDs across two
// pages. This records the rest: every endpoint, whatever its type or state,
// by ID and named after it.
func TestRecordVpcEndpointListsEndpoints(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeVpcEndpoints" {
			return recordUnexpected(t, call)
		}
		return recordEC2(call.Op, `<vpcEndpointSet>
			<item><vpcEndpointId>vpce-s3</vpcEndpointId><vpcEndpointType>Gateway</vpcEndpointType><vpcId>vpc-1</vpcId><serviceName>com.amazonaws.us-east-1.s3</serviceName><state>available</state></item>
			<item><vpcEndpointId>vpce-ssm</vpcEndpointId><vpcEndpointType>Interface</vpcEndpointType><vpcId>vpc-1</vpcId><serviceName>com.amazonaws.us-east-1.ssm</serviceName><state>pendingAcceptance</state></item>
			</vpcEndpointSet>`)
	})
	g := &VpcEndpointLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_vpc_endpoint", "vpce-s3", "vpce-s3", nil},
		recordedResource{"aws_vpc_endpoint", "vpce-ssm", "vpce-ssm", nil},
	)
}

func TestRecordVpcEndpointReturnsErrors(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return recordEC2Error()
	})
	g := &VpcEndpointLister{}

	assertRecordedError(t, g.List(), "UnauthorizedOperation")
}
