// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

const recordSQSURL = "https://sqs.us-east-1.amazonaws.com/123456789012/"

// TestSqsPaginates (pagination_test.go) pins the IDs across two pages and
// MaxResults. This records the names (the URL's last part) and the
// SQS_PREFIX environment variable, which, when set even to "", is the
// queue name prefix.
func TestRecordSqsListsQueuesByURL(t *testing.T) {
	for name, tc := range map[string]struct {
		set        bool
		prefix     string
		wantPrefix bool
	}{
		"no SQS_PREFIX":    {},
		"SQS_PREFIX":       {set: true, prefix: "orders", wantPrefix: true},
		"empty SQS_PREFIX": {set: true, prefix: "", wantPrefix: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SQS_PREFIX", tc.prefix)
			if !tc.set {
				if err := os.Unsetenv("SQS_PREFIX"); err != nil {
					t.Fatal(err)
				}
			}
			ctx := useFakeAPI(t, func(call apiCall) string {
				if call.Op != "ListQueues" {
					return recordUnexpected(t, call)
				}
				var body map[string]any
				if err := json.Unmarshal([]byte(call.Body), &body); err != nil {
					t.Fatal(err)
				}
				prefix, has := body["QueueNamePrefix"]
				if has != tc.wantPrefix || (has && prefix != tc.prefix) {
					t.Errorf("QueueNamePrefix %v (%t), want %q (%t)", prefix, has, tc.prefix, tc.wantPrefix)
				}
				return `{"QueueUrls":["` + recordSQSURL + `orders","` + recordSQSURL + `orders-dlq.fifo"]}`
			})
			g := &SqsLister{}

			resources, err := listChecked(ctx, t, g, harvest.Settings{})
			if err != nil {
				t.Fatal(err)
			}

			assertRecorded(t, resources,
				recordedResource{"aws_sqs_queue", recordSQSURL + "orders", "orders", nil},
				recordedResource{"aws_sqs_queue", recordSQSURL + "orders-dlq.fifo", "orders-dlq.fifo", nil},
			)
		})
	}
}

func TestRecordSqsReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDenied")
	})
	g := &SqsLister{}

	assertRecordedError(t, listError(ctx, g), "AccessDenied")
}
