// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

const recordDeviceFarmProject = "arn:aws:devicefarm:us-west-2:123456789012:project:"

// The lister lists each Device Farm project by ARN, named after its name,
// across pages.
func TestRecordDeviceFarmListsProjectsByARN(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "ListProjects" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return `{"projects":[{"arn":"` + recordDeviceFarmProject + `5e6f","name":"android app"}]}`
		}
		return fmt.Sprintf(`{"projects":[{"arn":"%s1a2b","name":"ios-app","defaultJobTimeoutMinutes":60}],"nextToken":%q}`,
			recordDeviceFarmProject, nextPageToken)
	})
	g := &DeviceFarmLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_devicefarm_project", recordDeviceFarmProject + "1a2b", "ios-app", nil},
		recordedResource{"aws_devicefarm_project", recordDeviceFarmProject + "5e6f", "android app", nil},
	)
}

func TestRecordDeviceFarmReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("ArgumentException")
	})
	g := &DeviceFarmLister{}

	assertRecordedError(t, listError(ctx, g), "ArgumentException")
}
