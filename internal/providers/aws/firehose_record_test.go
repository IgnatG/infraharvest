// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"
	"testing"
)

const recordFirehoseARN = "arn:aws:firehose:us-east-1:123456789012:deliverystream/"

// recordFirehoseStream is a DescribeDeliveryStream answer for the stream
// name in status.
func recordFirehoseStream(name, status string) string {
	return `{"DeliveryStreamDescription":{"DeliveryStreamName":"` + name + `","DeliveryStreamARN":"` + recordFirehoseARN + name +
		`","DeliveryStreamStatus":"` + status + `","DeliveryStreamType":"DirectPut","VersionId":"1","Destinations":[],"HasMoreDestinations":false}}`
}

// The lister lists delivery streams 100 at a time, each page starting
// after the last stream of the one before, and describes each for its
// ARN, which it records it by, with the name as an attribute. A stream
// being deleted, or whose description fails, is left out.
func TestRecordFirehoseListsDeliveryStreams(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListDeliveryStreams":
			if !strings.Contains(call.Body, `"Limit":100`) {
				t.Errorf("streams listed without a limit of 100: %s", call.Body)
			}
			switch recordJSONField(call, "ExclusiveStartDeliveryStreamName") {
			case "":
				return `{"DeliveryStreamNames":["orders","clicks","old"],"HasMoreDeliveryStreams":true}`
			case "old":
				return `{"DeliveryStreamNames":["audit","gone"],"HasMoreDeliveryStreams":false}`
			}
		case "DescribeDeliveryStream":
			switch name := recordJSONField(call, "DeliveryStreamName"); name {
			case "orders", "audit":
				return recordFirehoseStream(name, "ACTIVE")
			case "clicks":
				return recordFirehoseStream(name, "CREATING")
			case "old":
				return recordFirehoseStream(name, "DELETING")
			case "gone":
				return recordJSONError("ResourceNotFoundException")
			}
		}
		return recordUnexpected(t, call)
	})
	g := &FirehoseLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_kinesis_firehose_delivery_stream", recordFirehoseARN + "orders", "orders", map[string]string{"name": "orders"}},
		recordedResource{"aws_kinesis_firehose_delivery_stream", recordFirehoseARN + "clicks", "clicks", map[string]string{"name": "clicks"}},
		recordedResource{"aws_kinesis_firehose_delivery_stream", recordFirehoseARN + "audit", "audit", map[string]string{"name": "audit"}},
	)
}

func TestRecordFirehoseReturnsErrors(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDeniedException")
	})
	g := &FirehoseLister{}

	assertRecordedError(t, g.List(), "AccessDeniedException")
}
