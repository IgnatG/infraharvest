// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister lists each CloudHSM v2 cluster by ID, across pages, followed
// by its HSMs, each by ID with its cluster as an attribute. Clusters and
// HSMs being deleted, or deleted, are left out.
func TestRecordCloudHsmListsClustersAndHsms(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeClusters" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return `{"Clusters":[{"ClusterId":"cluster-old","State":"DELETED","HsmType":"hsm1.medium","Hsms":[]},
				{"ClusterId":"cluster-going","State":"DELETE_IN_PROGRESS","HsmType":"hsm1.medium","Hsms":[{"HsmId":"hsm-c","ClusterId":"cluster-going","State":"DELETE_IN_PROGRESS"}]}]}`
		}
		return fmt.Sprintf(`{"Clusters":[{"ClusterId":"cluster-1","State":"ACTIVE","HsmType":"hsm2m.medium","VpcId":"vpc-1",
			"Hsms":[
				{"HsmId":"hsm-a","ClusterId":"cluster-1","State":"ACTIVE","AvailabilityZone":"us-east-1a","SubnetId":"subnet-1"},
				{"HsmId":"hsm-b","ClusterId":"cluster-1","State":"CREATE_IN_PROGRESS","AvailabilityZone":"us-east-1b","SubnetId":"subnet-2"},
				{"HsmId":"hsm-d","ClusterId":"cluster-1","State":"DELETED","AvailabilityZone":"us-east-1c","SubnetId":"subnet-3"}]}],
			"NextToken":%q}`, nextPageToken)
	})
	g := &CloudHsmLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_cloudhsm_v2_cluster", "cluster-1", "cluster-1", nil},
		recordedResource{"aws_cloudhsm_v2_hsm", "hsm-a", "hsm-a", map[string]string{"cluster_id": "cluster-1"}},
		recordedResource{"aws_cloudhsm_v2_hsm", "hsm-b", "hsm-b", map[string]string{"cluster_id": "cluster-1"}},
	)
}

func TestRecordCloudHsmReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("CloudHsmAccessDeniedException")
	})
	g := &CloudHsmLister{}

	assertRecordedError(t, listError(ctx, g), "CloudHsmAccessDeniedException")
}
