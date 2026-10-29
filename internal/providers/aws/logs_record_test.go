// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister lists every log group by name, across pages, Lambda's own
// included, recording its retention and KMS key when it has them, and a
// group with neither as created (the catalogue skips Lambda's then).
func TestRecordLogsListsLogGroups(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "DescribeLogGroups" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return `{"logGroups":[
				{"logGroupName":"/aws/lambda/resize","arn":"arn:aws:logs:us-east-1:123456789012:log-group:/aws/lambda/resize:*","storedBytes":0},
				{"logGroupName":"audit","retentionInDays":3653},
				{"logGroupName":"/aws/lambda/orders","retentionInDays":14}]}`
		}
		return `{"logGroups":[{"logGroupName":"/app/web","retentionInDays":30,
			"kmsKeyId":"arn:aws:kms:us-east-1:123456789012:key/k-1","logGroupClass":"STANDARD"}],
			"nextToken":"` + nextPageToken + `"}`
	})
	g := &LogsLister{}

	resources, err := listChecked(ctx, t, g, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_cloudwatch_log_group", "/app/web", "/app/web", map[string]string{
			"retention_in_days": "30", "kms_key_id": "arn:aws:kms:us-east-1:123456789012:key/k-1"}},
		recordedResource{"aws_cloudwatch_log_group", "/aws/lambda/resize", "/aws/lambda/resize", map[string]string{"as_created": "true"}},
		recordedResource{"aws_cloudwatch_log_group", "audit", "audit", map[string]string{"retention_in_days": "3653"}},
		recordedResource{"aws_cloudwatch_log_group", "/aws/lambda/orders", "/aws/lambda/orders", map[string]string{"retention_in_days": "14"}},
	)
}

func TestRecordLogsReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDeniedException")
	})
	g := &LogsLister{}

	assertRecordedError(t, listError(ctx, g), "AccessDeniedException")
}
