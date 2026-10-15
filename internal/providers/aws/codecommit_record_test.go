// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"
)

// The lister lists repositories by name, then approval rule templates by
// name, each across pages.
func TestRecordCodeCommitListsRepositoriesAndTemplates(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListRepositories":
			if recordSecondPage(call) {
				return `{"repositories":[{"repositoryName":"infra","repositoryId":"r-3"}]}`
			}
			return fmt.Sprintf(`{"repositories":[
				{"repositoryName":"web","repositoryId":"r-1"},
				{"repositoryName":"api","repositoryId":"r-2"}],"nextToken":%q}`, nextPageToken)
		case "ListApprovalRuleTemplates":
			if recordSecondPage(call) {
				return `{"approvalRuleTemplateNames":["security-review"]}`
			}
			return fmt.Sprintf(`{"approvalRuleTemplateNames":["two-approvers"],"nextToken":%q}`, nextPageToken)
		}
		return recordUnexpected(t, call)
	})
	g := &CodeCommitLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_codecommit_repository", "web", "web", nil},
		recordedResource{"aws_codecommit_repository", "api", "api", nil},
		recordedResource{"aws_codecommit_repository", "infra", "infra", nil},
		recordedResource{"aws_codecommit_approval_rule_template", "two-approvers", "two-approvers", nil},
		recordedResource{"aws_codecommit_approval_rule_template", "security-review", "security-review", nil},
	)
}

// Each list fails the service.
func TestRecordCodeCommitReturnsErrors(t *testing.T) {
	for _, failing := range []string{"ListRepositories", "ListApprovalRuleTemplates"} {
		t.Run(failing, func(t *testing.T) {
			useFakeAPI(t, func(call apiCall) string {
				switch call.Op {
				case failing:
					return recordJSONError("AccessDeniedException")
				case "ListRepositories":
					return `{"repositories":[]}`
				}
				return recordUnexpected(t, call)
			})
			g := &CodeCommitLister{}

			assertRecordedError(t, g.List(), "AccessDeniedException")
		})
	}
}
