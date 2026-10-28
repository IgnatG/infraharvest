// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"net/url"
	"strings"
	"testing"
)

const (
	recordCfnNS       = "http://cloudformation.amazonaws.com/doc/2010-05-15/"
	recordCfnStack    = "arn:aws:cloudformation:us-east-1:123456789012:stack/"
	recordCfnBaseline = "baseline:4b3c1f0e-7d5a-4c2b-9e8f-1a2b3c4d5e6f"
)

// The lister lists stacks across pages but those deleted, nested stacks
// included, each with its name; it asks only for the live ones, and still
// leaves out a deleted one the answer has. Then it lists each stack set but
// the deleted ones, by name, followed by its instances, each list across
// pages.
func TestRecordCloudFormationListsStacksAndStackSets(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		form, _ := url.ParseQuery(call.Body)
		switch call.Op {
		case "ListStacks":
			if form.Get("StackStatusFilter.member.1") == "" {
				t.Errorf("stacks not listed by status: %s", call.Body)
			}
			for key, values := range form {
				if strings.HasPrefix(key, "StackStatusFilter.member.") && values[0] == "DELETE_COMPLETE" {
					t.Errorf("deleted stacks asked for: %s", call.Body)
				}
			}
			if recordSecondPage(call) {
				return recordQuery(call.Op, recordCfnNS, `<StackSummaries>
					<member><StackId>`+recordCfnStack+`db/2</StackId><StackName>db</StackName><StackStatus>UPDATE_ROLLBACK_COMPLETE</StackStatus></member>
					<member><StackId>`+recordCfnStack+`web-Assets-1ABC/3</StackId><StackName>web-Assets-1ABC</StackName><StackStatus>CREATE_COMPLETE</StackStatus>
						<ParentId>`+recordCfnStack+`web/1</ParentId><RootId>`+recordCfnStack+`web/1</RootId></member>
					</StackSummaries>`)
			}
			return recordQuery(call.Op, recordCfnNS, `<StackSummaries>
				<member><StackId>`+recordCfnStack+`web/1</StackId><StackName>web</StackName><StackStatus>CREATE_COMPLETE</StackStatus></member>
				<member><StackId>`+recordCfnStack+`old/0</StackId><StackName>old</StackName><StackStatus>DELETE_COMPLETE</StackStatus></member>
				<member><StackId>`+recordCfnStack+`broken/9</StackId><StackName>broken</StackName><StackStatus>ROLLBACK_COMPLETE</StackStatus></member>
				</StackSummaries><NextToken>`+nextPageToken+`</NextToken>`)
		case "ListStackSets":
			if recordSecondPage(call) {
				return recordQuery(call.Op, recordCfnNS, `<Summaries>
					<member><StackSetName>guardrails</StackSetName><StackSetId>guardrails:1a2b3c4d</StackSetId><Status>ACTIVE</Status></member>
					</Summaries>`)
			}
			return recordQuery(call.Op, recordCfnNS, `<Summaries>
				<member><StackSetName>baseline</StackSetName><StackSetId>`+recordCfnBaseline+`</StackSetId><Status>ACTIVE</Status></member>
				<member><StackSetName>retired</StackSetName><StackSetId>retired:0f0e0d0c</StackSetId><Status>DELETED</Status></member>
				</Summaries><NextToken>`+nextPageToken+`</NextToken>`)
		case "ListStackInstances":
			switch set := form.Get("StackSetName"); {
			case set == "baseline" && recordSecondPage(call):
				return recordQuery(call.Op, recordCfnNS, `<Summaries>
					<member><StackSetId>`+recordCfnBaseline+`</StackSetId><Account>222222222222</Account><Region>eu-west-1</Region><Status>OUTDATED</Status></member>
					</Summaries>`)
			case set == "baseline":
				return recordQuery(call.Op, recordCfnNS, `<Summaries>
					<member><StackSetId>`+recordCfnBaseline+`</StackSetId><Account>111111111111</Account><Region>us-east-1</Region><Status>CURRENT</Status></member>
					</Summaries><NextToken>`+nextPageToken+`</NextToken>`)
			case set == "guardrails":
				return recordQuery(call.Op, recordCfnNS, `<Summaries/>`)
			}
		}
		return recordUnexpected(t, call)
	})
	g := &CloudFormationLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_cloudformation_stack", recordCfnStack + "web/1", "web", map[string]string{"name": "web"}},
		recordedResource{"aws_cloudformation_stack", recordCfnStack + "broken/9", "broken", map[string]string{"name": "broken"}},
		recordedResource{"aws_cloudformation_stack", recordCfnStack + "db/2", "db", map[string]string{"name": "db"}},
		recordedResource{"aws_cloudformation_stack", recordCfnStack + "web-Assets-1ABC/3", "web-Assets-1ABC", map[string]string{"name": "web-Assets-1ABC"}},
		// Terraform imports a stack set, and its instances, by the set's
		// name, not its ID.
		recordedResource{"aws_cloudformation_stack_set", "baseline", "baseline", nil},
		recordedResource{"aws_cloudformation_stack_set_instance", "baseline,111111111111,us-east-1", "baseline,111111111111,us-east-1", nil},
		recordedResource{"aws_cloudformation_stack_set_instance", "baseline,222222222222,eu-west-1", "baseline,222222222222,eu-west-1", nil},
		recordedResource{"aws_cloudformation_stack_set", "guardrails", "guardrails", nil},
	)
}

// Each list fails the service.
func TestRecordCloudFormationReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"ListStacks", "ListStackSets", "ListStackInstances"} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, func(call apiCall) string {
				switch call.Op {
				case failing:
					return recordQueryError(recordCfnNS, "AccessDenied")
				case "ListStacks":
					return recordQuery(call.Op, recordCfnNS, `<StackSummaries/>`)
				case "ListStackSets":
					return recordQuery(call.Op, recordCfnNS, `<Summaries><member>
						<StackSetName>baseline</StackSetName><StackSetId>`+recordCfnBaseline+`</StackSetId><Status>ACTIVE</Status>
						</member></Summaries>`)
				}
				return recordUnexpected(t, call)
			})
			g := &CloudFormationLister{}
			g.SetContext(ctx)

			assertRecordedError(t, g.List(), "AccessDenied")
		})
	}
}
