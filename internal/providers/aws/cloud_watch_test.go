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
// CloudWatch over two pages each, and the rules of two event buses from
// EventBridge, the default one and the custom "orders": on the default
// bus a rule a service manages, left out with its targets, and two rules
// with targets; on "orders" a rule with a target and a managed rule. Rule
// and target pagination is pinned by TestCloudWatchEventTargetsPaginate.
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
		case "ListEventBuses":
			return `{"EventBuses":[{"Name":"default"},{"Name":"orders"}]}`
		case "ListRules":
			switch recordJSONField(call, "EventBusName") {
			case "":
				return `{"Rules":[
					{"Name":"nightly","Arn":"arn:aws:events:us-east-1:123456789012:rule/nightly","ScheduleExpression":"cron(0 2 * * ? *)","State":"ENABLED"},
					{"Name":"StepFunctionsGetEventsForStepFunctionsExecutionRule","ManagedBy":"states.amazonaws.com","State":"ENABLED"},
					{"Name":"on-upload","Arn":"arn:aws:events:us-east-1:123456789012:rule/on-upload","EventPattern":"{}","State":"DISABLED"}]}`
			case "orders":
				return `{"Rules":[
					{"Name":"nightly","Arn":"arn:aws:events:us-east-1:123456789012:rule/orders/nightly","EventPattern":"{}","State":"ENABLED"},
					{"Name":"AutogenerateForPartner","ManagedBy":"events.amazonaws.com","State":"ENABLED"}]}`
			}
		case "ListTargetsByRule":
			switch recordJSONField(call, "EventBusName") + "/" + recordJSONField(call, "Rule") {
			case "/nightly":
				return `{"Targets":[{"Id":"report","Arn":"arn:aws:lambda:us-east-1:123456789012:function:report"},
					{"Id":"audit","Arn":"arn:aws:sqs:us-east-1:123456789012:audit"}]}`
			case "/on-upload":
				return `{"Targets":[]}`
			case "orders/nightly":
				return `{"Targets":[{"Id":"report","Arn":"arn:aws:lambda:us-east-1:123456789012:function:report"}]}`
			}
		}
		return recordUnexpected(t, call)
	}
}

// The lister lists, in order, metric alarms and dashboards across pages,
// then the event rules of the default bus and of each custom bus, with
// the bus in the ID of those of a custom bus, each followed by its targets.
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
		recordedResource{"aws_cloudwatch_event_rule", "StepFunctionsGetEventsForStepFunctionsExecutionRule", "StepFunctionsGetEventsForStepFunctionsExecutionRule",
			map[string]string{"managed_by": "states.amazonaws.com"}},
		recordedResource{"aws_cloudwatch_event_rule", "on-upload", "on-upload", nil},
		recordedResource{"aws_cloudwatch_event_rule", "orders/nightly", "orders/nightly", nil},
		recordedResource{"aws_cloudwatch_event_target", "orders/nightly/report", "orders/nightly/report",
			map[string]string{"rule": "nightly", "target_id": "report", "event_bus_name": "orders"}},
		recordedResource{"aws_cloudwatch_event_rule", "orders/AutogenerateForPartner", "orders/AutogenerateForPartner",
			map[string]string{"managed_by": "events.amazonaws.com"}},
	)
}

// A custom bus whose rules can't be read leaves a partial listing with the
// other buses' rules and targets; a bus deleted since it was listed is no
// failure; and a failing bus list leaves the default bus listed.
func TestRecordCloudWatchCustomBusFailuresArePartial(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		code    string
		failing string
		partial bool
	}{
		"rules denied": {"AccessDeniedException", "orders", true},
		"bus gone":     {"ResourceNotFoundException", "orders", false},
		"buses denied": {"AccessDeniedException", "", true},
	} {
		t.Run(name, func(t *testing.T) {
			fake := recordCloudWatchAPI(t, "")
			ctx := useFakeAPI(t, func(call apiCall) string {
				switch {
				case call.Op == "ListEventBuses" && tc.failing == "":
					return recordJSONError(tc.code)
				case call.Op == "ListRules" && tc.failing != "" && recordJSONField(call, "EventBusName") == tc.failing:
					return recordJSONError(tc.code)
				}
				return fake(call)
			})
			resources, err := listChecked(ctx, t, &CloudWatchLister{}, harvest.Settings{})
			if tc.partial != harvest.IsPartial(err) || (err != nil && !tc.partial) {
				t.Fatalf("error %v, want partial: %v", err, tc.partial)
			}
			assertIDs(t, resources, "aws_cloudwatch_event_rule", "nightly", "StepFunctionsGetEventsForStepFunctionsExecutionRule", "on-upload")
		})
	}
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
