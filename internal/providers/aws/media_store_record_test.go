// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister lists every MediaStore container by name, across pages, but
// those being deleted.
func TestRecordMediaStoreListsContainers(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "ListContainers" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return `{"Containers":[
				{"Name":"archive","ARN":"arn:aws:mediastore:us-east-1:123456789012:container/archive","Status":"CREATING"},
				{"Name":"old","ARN":"arn:aws:mediastore:us-east-1:123456789012:container/old","Status":"DELETING"}]}`
		}
		return `{"Containers":[{"Name":"live","ARN":"arn:aws:mediastore:us-east-1:123456789012:container/live",
			"Endpoint":"https://abc123.data.mediastore.us-east-1.amazonaws.com","Status":"ACTIVE","AccessLoggingEnabled":true}],
			"NextToken":"` + nextPageToken + `"}`
	})
	g := &MediaStoreLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_media_store_container", "live", "live", nil},
		recordedResource{"aws_media_store_container", "archive", "archive", nil},
	)
}

func TestRecordMediaStoreReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("InternalServerError")
	})
	g := &MediaStoreLister{}

	assertRecordedError(t, listError(ctx, g), "InternalServerError")
}
