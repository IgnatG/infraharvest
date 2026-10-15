// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

const recordCloudTrailARN = "arn:aws:cloudtrail:"

// The lister lists the trails of its region by ARN, named after their
// names, asking for no shadow trails, and leaves out a trail whose home is
// another region, in case one is answered anyway: it is that region's.
func TestRecordCloudTrailListsTheRegionsOwnTrails(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeTrails" {
			return recordUnexpected(t, call)
		}
		if shadows, set := recordJSONBool(call, "includeShadowTrails"); !set || shadows {
			t.Errorf("trails listed with their shadows: %s", call.Body)
		}
		return `{"trailList":[
			{"Name":"audit","TrailARN":"` + recordCloudTrailARN + `us-east-1:123456789012:trail/audit","HomeRegion":"us-east-1","IsMultiRegionTrail":true},
			{"Name":"local","TrailARN":"` + recordCloudTrailARN + `us-east-1:123456789012:trail/local","HomeRegion":"us-east-1"},
			{"Name":"elsewhere","TrailARN":"` + recordCloudTrailARN + `eu-west-1:123456789012:trail/elsewhere","HomeRegion":"eu-west-1","IsMultiRegionTrail":true}]}`
	})
	g := &CloudTrailLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_cloudtrail", recordCloudTrailARN + "us-east-1:123456789012:trail/audit", "audit", nil},
		recordedResource{"aws_cloudtrail", recordCloudTrailARN + "us-east-1:123456789012:trail/local", "local", nil},
	)
}

func TestRecordCloudTrailReturnsErrors(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDeniedException")
	})
	g := &CloudTrailLister{}

	assertRecordedError(t, g.List(), "AccessDeniedException")
}
