// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"
)

// The lister lists every analyzer, whatever its type or status, across
// pages, by name, with no attributes. Archive rules are not listed.
func TestRecordAccessAnalyzerListsAnalyzersByName(t *testing.T) {
	const arn = "arn:aws:access-analyzer:us-east-1:123456789012:analyzer/"
	useFakeAPI(t, func(call apiCall) string {
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

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_accessanalyzer_analyzer", "account", "account", nil},
		recordedResource{"aws_accessanalyzer_analyzer", "org", "org", nil},
		recordedResource{"aws_accessanalyzer_analyzer", "org-unused", "org-unused", nil},
	)
}

func TestRecordAccessAnalyzerReturnsErrors(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDeniedException")
	})
	g := &AccessAnalyzerLister{}

	assertRecordedError(t, g.List(), "AccessDeniedException")
}
