// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

// The lister lists each Elastic IP by allocation ID, associated or not,
// from one DescribeAddresses call. An EC2-Classic address, which has no
// allocation ID, is left out. An address associated with an instance is
// named after the instance's ID, which the import names after the
// instance; before, every address was labelled eipalloc_....
func TestRecordElasticIPListsAddressesByAllocationID(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeAddresses" {
			return recordUnexpected(t, call)
		}
		return recordEC2(call.Op, `<addressesSet>
			<item><publicIp>203.0.113.10</publicIp><allocationId>eipalloc-1</allocationId><domain>vpc</domain>
				<associationId>eipassoc-1</associationId><instanceId>i-1</instanceId></item>
			<item><publicIp>203.0.113.11</publicIp><allocationId>eipalloc-2</allocationId><domain>vpc</domain></item>
			<item><publicIp>198.51.100.7</publicIp><domain>standard</domain></item>
			</addressesSet>`)
	})
	g := &ElasticIPLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_eip", "eipalloc-1", "i-1", nil},
		recordedResource{"aws_eip", "eipalloc-2", "eipalloc-2", nil},
	)
}

func TestRecordElasticIPReturnsErrors(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return recordEC2Error()
	})
	g := &ElasticIPLister{}

	assertRecordedError(t, g.List(), "UnauthorizedOperation")
}
