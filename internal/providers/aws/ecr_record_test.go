// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

const recordEcrRepo = "arn:aws:ecr:us-east-1:123456789012:repository/"

// The lister lists repositories by name, across pages, each followed by
// its repository policy and its lifecycle policy, both by the repository's
// name, when reading them succeeds. ECR's answer that there is no such
// policy, or no longer such a repository, means there is none; any other
// error is returned as a partial one, naming the repository, and the other
// repositories are still listed.
func TestRecordEcrListsRepositoriesAndPolicies(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		repository := recordJSONField(call, "repositoryName")
		if call.Op != "DescribeRepositories" && recordJSONField(call, "registryId") != "123456789012" {
			t.Errorf("%s of %s outside the repository's registry: %s", call.Op, repository, call.Body)
		}
		switch call.Op {
		case "DescribeRepositories":
			if recordSecondPage(call) {
				return `{"repositories":[
					{"repositoryName":"api","registryId":"123456789012","repositoryArn":"` + recordEcrRepo + `api","repositoryUri":"123456789012.dkr.ecr.us-east-1.amazonaws.com/api"},
					{"repositoryName":"tools/legacy","registryId":"123456789012","repositoryArn":"` + recordEcrRepo + `tools/legacy"},
					{"repositoryName":"gone","registryId":"123456789012","repositoryArn":"` + recordEcrRepo + `gone"}]}`
			}
			return fmt.Sprintf(`{"repositories":[{"repositoryName":"web","registryId":"123456789012","repositoryArn":"%sweb",
				"imageTagMutability":"IMMUTABLE"}],"nextToken":%q}`, recordEcrRepo, nextPageToken)
		case "GetRepositoryPolicy":
			switch repository {
			case "web", "tools/legacy":
				return `{"registryId":"123456789012","repositoryName":"` + repository + `","policyText":"{\"Version\":\"2012-10-17\"}"}`
			case "api":
				return recordJSONError("RepositoryPolicyNotFoundException")
			case "gone":
				return recordJSONError("RepositoryNotFoundException")
			}
		case "GetLifecyclePolicy":
			switch repository {
			case "api":
				return `{"registryId":"123456789012","repositoryName":"api","lifecyclePolicyText":"{\"rules\":[]}"}`
			case "web":
				return recordJSONError("LifecyclePolicyNotFoundException")
			case "tools/legacy":
				return recordJSONError("AccessDeniedException")
			case "gone":
				return recordJSONError("RepositoryNotFoundException")
			}
		}
		return recordUnexpected(t, call)
	})
	g := &EcrLister{}
	g.SetContext(ctx)

	err := g.List()
	if !harvest.IsPartial(err) || !strings.Contains(err.Error(), "the lifecycle policy of tools/legacy") || !strings.Contains(err.Error(), "AccessDeniedException") {
		t.Errorf("want a partial error for the lifecycle policy of tools/legacy, got %v", err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_ecr_repository", "web", "web", nil},
		recordedResource{"aws_ecr_repository_policy", "web", "web", nil},
		recordedResource{"aws_ecr_repository", "api", "api", nil},
		recordedResource{"aws_ecr_lifecycle_policy", "api", "api", nil},
		recordedResource{"aws_ecr_repository", "tools/legacy", "tools/legacy", nil},
		recordedResource{"aws_ecr_repository_policy", "tools/legacy", "tools/legacy", nil},
		recordedResource{"aws_ecr_repository", "gone", "gone", nil},
	)
}

func TestRecordEcrReturnsRepositoryErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDeniedException")
	})
	g := &EcrLister{}
	g.SetContext(ctx)

	assertRecordedError(t, g.List(), "AccessDeniedException")
}
