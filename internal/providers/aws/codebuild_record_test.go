// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"
)

// The lister lists each CodeBuild project by name, across pages.
func TestRecordCodeBuildListsProjectsByName(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "ListProjects" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return `{"projects":["api-build"]}`
		}
		return fmt.Sprintf(`{"projects":["web-build","web-tests"],"nextToken":%q}`, nextPageToken)
	})
	g := &CodeBuildLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_codebuild_project", "web-build", "web-build", nil},
		recordedResource{"aws_codebuild_project", "web-tests", "web-tests", nil},
		recordedResource{"aws_codebuild_project", "api-build", "api-build", nil},
	)
}

func TestRecordCodeBuildReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDeniedException")
	})
	g := &CodeBuildLister{}
	g.SetContext(ctx)

	assertRecordedError(t, g.List(), "AccessDeniedException")
}
