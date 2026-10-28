// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

const recordFirehoseARN = "arn:aws:firehose:us-east-1:123456789012:deliverystream/"

// recordCallerIdentity is GetCallerIdentity's answer for account
// 123456789012.
var recordCallerIdentity = recordQuery("GetCallerIdentity", "https://sts.amazonaws.com/doc/2011-06-15/",
	`<Account>123456789012</Account><Arn>arn:aws:iam::123456789012:user/dev</Arn><UserId>AIDA</UserId>`)

// The lister lists delivery streams 100 at a time, each page starting
// after the last stream of the one before, and records each by its ARN,
// built from its name, the region and the account, which it asks for once,
// with the name as an attribute. No stream is described.
func TestRecordFirehoseListsDeliveryStreams(t *testing.T) {
	t.Parallel()
	identities := 0
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "GetCallerIdentity":
			identities++
			return recordCallerIdentity
		case "ListDeliveryStreams":
			if !strings.Contains(call.Body, `"Limit":100`) {
				t.Errorf("streams listed without a limit of 100: %s", call.Body)
			}
			switch recordJSONField(call, "ExclusiveStartDeliveryStreamName") {
			case "":
				return `{"DeliveryStreamNames":["orders","clicks"],"HasMoreDeliveryStreams":true}`
			case "clicks":
				return `{"DeliveryStreamNames":["audit"],"HasMoreDeliveryStreams":false}`
			}
		}
		return recordUnexpected(t, call)
	})
	g := &FirehoseLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_kinesis_firehose_delivery_stream", recordFirehoseARN + "orders", "orders", map[string]string{"name": "orders"}},
		recordedResource{"aws_kinesis_firehose_delivery_stream", recordFirehoseARN + "clicks", "clicks", map[string]string{"name": "clicks"}},
		recordedResource{"aws_kinesis_firehose_delivery_stream", recordFirehoseARN + "audit", "audit", map[string]string{"name": "audit"}},
	)
	if identities != 1 {
		t.Errorf("GetCallerIdentity asked %d times, want once", identities)
	}
}

// A region without streams doesn't ask for the account.
func TestRecordFirehoseWithoutStreams(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op == "ListDeliveryStreams" {
			return `{"DeliveryStreamNames":[],"HasMoreDeliveryStreams":false}`
		}
		return recordUnexpected(t, call)
	})
	g := &FirehoseLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	assertRecorded(t, resources)
}

// Failing to list the streams, or to find the account, fails the service.
func TestRecordFirehoseReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"ListDeliveryStreams", "GetCallerIdentity"} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, func(call apiCall) string {
				switch call.Op {
				case "GetCallerIdentity":
					if failing == call.Op {
						return recordQueryError("https://sts.amazonaws.com/doc/2011-06-15/", "AccessDenied")
					}
					return recordCallerIdentity
				case "ListDeliveryStreams":
					if failing == call.Op {
						return recordJSONError("AccessDenied")
					}
					return `{"DeliveryStreamNames":["orders"],"HasMoreDeliveryStreams":false}`
				}
				return recordUnexpected(t, call)
			})
			g := &FirehoseLister{}

			assertRecordedError(t, listError(ctx, g), "AccessDenied")
		})
	}
}

// A region's partition is the one its name tells.
func TestPartitionOf(t *testing.T) {
	t.Parallel()
	for region, want := range map[string]string{
		"eu-west-2":      "aws",
		"us-east-1":      "aws",
		"cn-north-1":     "aws-cn",
		"us-gov-west-1":  "aws-us-gov",
		"us-iso-east-1":  "aws-iso",
		"us-isob-east-1": "aws-iso-b",
	} {
		if got := partitionOf(region); got != want {
			t.Errorf("partitionOf(%q) = %q, want %q", region, got, want)
		}
	}
}
