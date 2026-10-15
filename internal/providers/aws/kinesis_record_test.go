// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

// TestKinesisPaginates (pagination_test.go) pins the IDs across two pages.
// This records the rest: each stream by name, with its name as an
// attribute, from ListStreams' StreamNames.
func TestRecordKinesisListsStreamsByName(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op != "ListStreams" {
			return recordUnexpected(t, call)
		}
		return `{"StreamNames":["orders","clicks"],"HasMoreStreams":false,
			"StreamSummaries":[
				{"StreamName":"orders","StreamARN":"arn:aws:kinesis:us-east-1:123456789012:stream/orders","StreamStatus":"ACTIVE"},
				{"StreamName":"clicks","StreamARN":"arn:aws:kinesis:us-east-1:123456789012:stream/clicks","StreamStatus":"ACTIVE"}]}`
	})
	g := &KinesisLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_kinesis_stream", "orders", "orders", map[string]string{"name": "orders"}},
		recordedResource{"aws_kinesis_stream", "clicks", "clicks", map[string]string{"name": "clicks"}},
	)
}

func TestRecordKinesisReturnsErrors(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDeniedException")
	})
	g := &KinesisLister{}

	assertRecordedError(t, g.List(), "AccessDeniedException")
}
