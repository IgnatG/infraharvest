// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

const recordAddresses = `<addressesSet>
	<item><publicIp>203.0.113.10</publicIp><allocationId>eipalloc-1</allocationId><domain>vpc</domain>
		<associationId>eipassoc-1</associationId><instanceId>i-1</instanceId></item>
	<item><publicIp>203.0.113.11</publicIp><allocationId>eipalloc-2</allocationId><domain>vpc</domain></item>
	<item><publicIp>203.0.113.12</publicIp><allocationId>eipalloc-3</allocationId><domain>vpc</domain>
		<associationId>eipassoc-3</associationId><networkInterfaceId>eni-3</networkInterfaceId></item>
	<item><publicIp>198.51.100.7</publicIp><domain>standard</domain></item>
	</addressesSet>`

// The lister lists each Elastic IP by allocation ID, associated or not,
// from one DescribeAddresses call. An EC2-Classic address, which has no
// allocation ID, is left out. Each is named after the instance it is
// associated with or the NAT gateway that uses it, by ID, which the import
// names after that resource; else after its allocation ID. Before, every
// address was named after its allocation ID, labelled eipalloc_....
func TestRecordElasticIPListsAddressesByAllocationID(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "DescribeAddresses":
			return recordEC2(call.Op, recordAddresses)
		case "DescribeNatGateways":
			return recordEC2(call.Op, `<natGatewaySet>
				<item><natGatewayId>nat-1</natGatewayId><natGatewayAddressSet>
					<item><allocationId>eipalloc-3</allocationId><networkInterfaceId>eni-3</networkInterfaceId></item>
				</natGatewayAddressSet></item>
				</natGatewaySet>`)
		}
		return recordUnexpected(t, call)
	})
	g := &ElasticIPLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_eip", "eipalloc-1", "i-1", nil},
		recordedResource{"aws_eip", "eipalloc-2", "eipalloc-2", nil},
		recordedResource{"aws_eip", "eipalloc-3", "nat-1", nil},
	)
}

// NAT gateways that can't be listed only cost the addresses their names.
func TestRecordElasticIPNamesWithoutNATGateways(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op == "DescribeAddresses" {
			return recordEC2(call.Op, recordAddresses)
		}
		return recordEC2Error()
	})
	g := &ElasticIPLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_eip", "eipalloc-1", "i-1", nil},
		recordedResource{"aws_eip", "eipalloc-2", "eipalloc-2", nil},
		recordedResource{"aws_eip", "eipalloc-3", "eipalloc-3", nil},
	)
}

func TestRecordElasticIPReturnsErrors(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return recordEC2Error()
	})
	g := &ElasticIPLister{}

	assertRecordedError(t, g.List(), "UnauthorizedOperation")
}
