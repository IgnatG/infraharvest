// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

// The lister lists every log group by name, across pages, Lambda's own
// included (the catalogue skips those), recording its retention and KMS key
// when it has them.
func TestRecordLogsListsLogGroups(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeLogGroups" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return `{"logGroups":[
				{"logGroupName":"/aws/lambda/resize","arn":"arn:aws:logs:us-east-1:123456789012:log-group:/aws/lambda/resize:*","storedBytes":0},
				{"logGroupName":"audit","retentionInDays":3653}]}`
		}
		return `{"logGroups":[{"logGroupName":"/app/web","retentionInDays":30,
			"kmsKeyId":"arn:aws:kms:us-east-1:123456789012:key/k-1","logGroupClass":"STANDARD"}],
			"nextToken":"` + nextPageToken + `"}`
	})
	g := &LogsLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_cloudwatch_log_group", "/app/web", "/app/web", map[string]string{
			"retention_in_days": "30", "kms_key_id": "arn:aws:kms:us-east-1:123456789012:key/k-1"}},
		recordedResource{"aws_cloudwatch_log_group", "/aws/lambda/resize", "/aws/lambda/resize", nil},
		recordedResource{"aws_cloudwatch_log_group", "audit", "audit", map[string]string{"retention_in_days": "3653"}},
	)
}

func TestRecordLogsReturnsErrors(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDeniedException")
	})
	g := &LogsLister{}

	assertRecordedError(t, g.List(), "AccessDeniedException")
}
