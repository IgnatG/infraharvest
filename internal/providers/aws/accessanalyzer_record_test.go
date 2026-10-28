// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister lists every analyzer, whatever its type or status, across
// pages, by name, with no attributes. Archive rules are not listed.
func TestRecordAccessAnalyzerListsAnalyzersByName(t *testing.T) {
	t.Parallel()
	const arn = "arn:aws:access-analyzer:us-east-1:123456789012:analyzer/"
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Path != "/analyzer" {
			return recordUnexpected(t, call)
		}
		if call.Query.Get("type") != "" {
			t.Errorf("analyzers listed by type: %s", call.Query.Encode())
		}
		if recordSecondPage(call) {
			return `{"analyzers":[
				{"name":"org-unused","arn":"` + arn + `org-unused","type":"ORGANIZATION_UNUSED_ACCESS","status":"FAILED"}]}`
		}
		return fmt.Sprintf(`{"analyzers":[
			{"name":"account","arn":"%saccount","type":"ACCOUNT","status":"ACTIVE"},
			{"name":"org","arn":"%sorg","type":"ORGANIZATION","status":"CREATING"}],"nextToken":%q}`, arn, arn, nextPageToken)
	})
	g := &AccessAnalyzerLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_accessanalyzer_analyzer", "account", "account", nil},
		recordedResource{"aws_accessanalyzer_analyzer", "org", "org", nil},
		recordedResource{"aws_accessanalyzer_analyzer", "org-unused", "org-unused", nil},
	)
}

func TestRecordAccessAnalyzerReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDeniedException")
	})
	g := &AccessAnalyzerLister{}

	assertRecordedError(t, listError(ctx, g), "AccessDeniedException")
}
