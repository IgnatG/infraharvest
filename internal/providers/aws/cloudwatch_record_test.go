// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"

	"github.com/aws/smithy-go/encoding/cbor"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// recordCloudWatchPage answers a CloudWatch list with items under listKey,
// and token as NextToken unless it is empty.
func recordCloudWatchPage(listKey string, items cbor.List, token string) string {
	answer := cbor.Map{listKey: items}
	if token != "" {
		answer["NextToken"] = cbor.String(token)
	}
	return string(cbor.Encode(answer))
}

// recordCloudWatchAPI answers the metric alarms and dashboards from
// CloudWatch over two pages each, and the default event bus's rules from
// EventBridge: a rule a service manages, left out with its targets, and
// two rules with targets. Rule and target pagination is pinned by
// TestCloudWatchEventTargetsPaginate.
func recordCloudWatchAPI(t *testing.T, failing string) func(apiCall) string {
	return func(call apiCall) string {
		op := call.Op
		if failing != "" && op == failing {
			return recordJSONError("AccessDeniedException")
		}
		switch op {
		case "DescribeAlarms":
			if recordSecondPage(call) {
				return recordCloudWatchPage("MetricAlarms", cbor.List{
					cbor.Map{"AlarmName": cbor.String("queue-depth"), "StateValue": cbor.String("ALARM")},
				}, "")
			}
			return recordCloudWatchPage("MetricAlarms", cbor.List{
				cbor.Map{"AlarmName": cbor.String("cpu-high"), "StateValue": cbor.String("OK")},
				cbor.Map{"AlarmName": cbor.String("5xx rate"), "StateValue": cbor.String("INSUFFICIENT_DATA")},
			}, nextPageToken)
		case "ListDashboards":
			if recordSecondPage(call) {
				return recordCloudWatchPage("DashboardEntries", cbor.List{
					cbor.Map{"DashboardName": cbor.String("billing")},
				}, "")
			}
			return recordCloudWatchPage("DashboardEntries", cbor.List{
				cbor.Map{"DashboardName": cbor.String("ops"), "DashboardArn": cbor.String("arn:aws:cloudwatch::123456789012:dashboard/ops")},
			}, nextPageToken)
		case "ListRules":
			if got := recordJSONField(call, "EventBusName"); got != "" {
				t.Errorf("rules of event bus %q, want the default bus", got)
			}
			return `{"Rules":[
				{"Name":"nightly","Arn":"arn:aws:events:us-east-1:123456789012:rule/nightly","ScheduleExpression":"cron(0 2 * * ? *)","State":"ENABLED"},
				{"Name":"StepFunctionsGetEventsForStepFunctionsExecutionRule","ManagedBy":"states.amazonaws.com","State":"ENABLED"},
				{"Name":"on-upload","Arn":"arn:aws:events:us-east-1:123456789012:rule/on-upload","EventPattern":"{}","State":"DISABLED"}]}`
		case "ListTargetsByRule":
			switch recordJSONField(call, "Rule") {
			case "nightly":
				return `{"Targets":[{"Id":"report","Arn":"arn:aws:lambda:us-east-1:123456789012:function:report"},
					{"Id":"audit","Arn":"arn:aws:sqs:us-east-1:123456789012:audit"}]}`
			case "on-upload":
				return `{"Targets":[]}`
			}
		}
		return recordUnexpected(t, call)
	}
}

// The lister lists, in order, metric alarms and dashboards across pages,
// then the default bus's event rules but those a service manages, each
// followed by its targets.
func TestRecordCloudWatchListsAlarmsDashboardsRulesAndTargets(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordCloudWatchAPI(t, ""))
	g := &CloudWatchLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_cloudwatch_metric_alarm", "cpu-high", "cpu-high", nil},
		recordedResource{"aws_cloudwatch_metric_alarm", "5xx rate", "5xx rate", nil},
		recordedResource{"aws_cloudwatch_metric_alarm", "queue-depth", "queue-depth", nil},
		recordedResource{"aws_cloudwatch_dashboard", "ops", "ops", nil},
		recordedResource{"aws_cloudwatch_dashboard", "billing", "billing", nil},
		recordedResource{"aws_cloudwatch_event_rule", "nightly", "nightly", nil},
		recordedResource{"aws_cloudwatch_event_target", "nightly/report", "nightly/report",
			map[string]string{"rule": "nightly", "target_id": "report"}},
		recordedResource{"aws_cloudwatch_event_target", "nightly/audit", "nightly/audit",
			map[string]string{"rule": "nightly", "target_id": "audit"}},
		recordedResource{"aws_cloudwatch_event_rule", "on-upload", "on-upload", nil},
	)
}

// The EventBridge lists fail the service. (The fake API cannot answer a
// CBOR error, so the CloudWatch lists' errors are not recorded here.)
func TestRecordCloudWatchReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"ListRules", "ListTargetsByRule"} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, recordCloudWatchAPI(t, failing))
			g := &CloudWatchLister{}

			assertRecordedError(t, listError(ctx, g), "AccessDeniedException")
		})
	}
}
