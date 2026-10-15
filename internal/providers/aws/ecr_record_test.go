// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"
)

const recordEcrRepo = "arn:aws:ecr:us-east-1:123456789012:repository/"

// The lister lists repositories by name, across pages, each followed by
// its repository policy and its lifecycle policy, both by the repository's
// name, when reading them succeeds; any error means the repository has
// none.
func TestRecordEcrListsRepositoriesAndPolicies(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		repository := recordJSONField(call, "repositoryName")
		if call.Op != "DescribeRepositories" && recordJSONField(call, "registryId") != "123456789012" {
			t.Errorf("%s of %s outside the repository's registry: %s", call.Op, repository, call.Body)
		}
		switch call.Op {
		case "DescribeRepositories":
			if recordSecondPage(call) {
				return `{"repositories":[
					{"repositoryName":"api","registryId":"123456789012","repositoryArn":"` + recordEcrRepo + `api","repositoryUri":"123456789012.dkr.ecr.us-east-1.amazonaws.com/api"},
					{"repositoryName":"tools/legacy","registryId":"123456789012","repositoryArn":"` + recordEcrRepo + `tools/legacy"}]}`
			}
			return fmt.Sprintf(`{"repositories":[{"repositoryName":"web","registryId":"123456789012","repositoryArn":"%sweb",
				"imageTagMutability":"IMMUTABLE"}],"nextToken":%q}`, recordEcrRepo, nextPageToken)
		case "GetRepositoryPolicy":
			switch repository {
			case "web", "tools/legacy":
				return `{"registryId":"123456789012","repositoryName":"` + repository + `","policyText":"{\"Version\":\"2012-10-17\"}"}`
			case "api":
				return recordJSONError("RepositoryPolicyNotFoundException")
			}
		case "GetLifecyclePolicy":
			switch repository {
			case "api":
				return `{"registryId":"123456789012","repositoryName":"api","lifecyclePolicyText":"{\"rules\":[]}"}`
			case "web":
				return recordJSONError("LifecyclePolicyNotFoundException")
			case "tools/legacy":
				return recordJSONError("AccessDeniedException")
			}
		}
		return recordUnexpected(t, call)
	})
	g := &EcrLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_ecr_repository", "web", "web", nil},
		recordedResource{"aws_ecr_repository_policy", "web", "web", nil},
		recordedResource{"aws_ecr_repository", "api", "api", nil},
		recordedResource{"aws_ecr_lifecycle_policy", "api", "api", nil},
		recordedResource{"aws_ecr_repository", "tools/legacy", "tools/legacy", nil},
		recordedResource{"aws_ecr_repository_policy", "tools/legacy", "tools/legacy", nil},
	)
}

func TestRecordEcrReturnsRepositoryErrors(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDeniedException")
	})
	g := &EcrLister{}

	assertRecordedError(t, g.List(), "AccessDeniedException")
}
