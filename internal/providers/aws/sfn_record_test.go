// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

const recordSfnARN = "arn:aws:states:us-east-1:123456789012:"

func recordSfnAPI(t *testing.T, failing string) func(apiCall) string {
	return func(call apiCall) string {
		if call.Op == failing {
			return recordJSONError("AccessDeniedException")
		}
		switch call.Op {
		case "ListStateMachines":
			name, token := "orders", nextPageToken
			if recordSecondPage(call) {
				name, token = "refunds", ""
			}
			return fmt.Sprintf(`{"stateMachines":[{"stateMachineArn":"%sstateMachine:%s","name":%q,"type":"STANDARD"}],"nextToken":%q}`,
				recordSfnARN, name, name, token)
		case "ListActivities":
			name, token := "approve", nextPageToken
			if recordSecondPage(call) {
				name, token = "review", ""
			}
			return fmt.Sprintf(`{"activities":[{"activityArn":"%sactivity:%s","name":%q}],"nextToken":%q}`,
				recordSfnARN, name, name, token)
		}
		return recordUnexpected(t, call)
	}
}

// The lister lists state machines, then activities, by ARN, named after
// their names, across pages.
func TestRecordSfnListsStateMachinesAndActivities(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordSfnAPI(t, ""))
	g := &SfnLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_sfn_state_machine", recordSfnARN + "stateMachine:orders", "orders", nil},
		recordedResource{"aws_sfn_state_machine", recordSfnARN + "stateMachine:refunds", "refunds", nil},
		recordedResource{"aws_sfn_activity", recordSfnARN + "activity:approve", "approve", nil},
		recordedResource{"aws_sfn_activity", recordSfnARN + "activity:review", "review", nil},
	)
}

func TestRecordSfnReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"ListStateMachines", "ListActivities"} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, recordSfnAPI(t, failing))
			g := &SfnLister{}

			assertRecordedError(t, listError(ctx, g), "AccessDeniedException")
		})
	}
}
