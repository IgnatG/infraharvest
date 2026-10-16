// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

// TestVpcEndpointPaginates (pagination_test.go) pins the IDs across two
// pages. This records the rest: every endpoint, whatever its type or state,
// by ID and named after its service: s3, not vpce_s3, which was the label
// before. An endpoint service of another account is named after the
// endpoint's ID.
func TestRecordVpcEndpointListsEndpoints(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeVpcEndpoints" {
			return recordUnexpected(t, call)
		}
		return recordEC2(call.Op, `<vpcEndpointSet>
			<item><vpcEndpointId>vpce-s3</vpcEndpointId><vpcEndpointType>Gateway</vpcEndpointType><vpcId>vpc-1</vpcId><serviceName>com.amazonaws.us-east-1.s3</serviceName><state>available</state></item>
			<item><vpcEndpointId>vpce-ssm</vpcEndpointId><vpcEndpointType>Interface</vpcEndpointType><vpcId>vpc-1</vpcId><serviceName>com.amazonaws.us-east-1.ssm</serviceName><state>pendingAcceptance</state></item>
			<item><vpcEndpointId>vpce-svc</vpcEndpointId><vpcEndpointType>Interface</vpcEndpointType><vpcId>vpc-1</vpcId><serviceName>com.amazonaws.vpce.us-east-1.vpce-svc-0123456789abcdef0</serviceName><state>available</state></item>
			</vpcEndpointSet>`)
	})
	g := &VpcEndpointLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_vpc_endpoint", "vpce-s3", "s3", nil},
		recordedResource{"aws_vpc_endpoint", "vpce-ssm", "ssm", nil},
		recordedResource{"aws_vpc_endpoint", "vpce-svc", "vpce-svc", nil},
	)
}

func TestRecordVpcEndpointReturnsErrors(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return recordEC2Error()
	})
	g := &VpcEndpointLister{}

	assertRecordedError(t, g.List(), "UnauthorizedOperation")
}

func TestVpcEndpointName(t *testing.T) {
	for service, want := range map[string]string{
		"com.amazonaws.eu-west-2.s3":                      "s3",
		"com.amazonaws.eu-west-2.ecr.dkr":                 "ecr.dkr",
		"com.amazonaws.us-gov-east-1.ssm":                 "ssm",
		"cn.com.amazonaws.cn-north-1.s3":                  "s3",
		"com.amazonaws.vpce.eu-west-2.vpce-svc-0a1b2c3d4": "vpce-1",
		"aws.sagemaker.eu-west-2.notebook":                "notebook",
		"":                                                "vpce-1",
	} {
		if got := vpcEndpointName(service, "vpce-1"); got != want {
			t.Errorf("vpcEndpointName(%q): got %q, want %q", service, got, want)
		}
	}
}
