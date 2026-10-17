// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// recordCloud9Lifecycle is the lifecycle status the fake Cloud9 API
// describes each environment with. An environment missing here is
// described without a lifecycle.
var recordCloud9Lifecycle = map[string]string{
	"env-created":       "CREATED",
	"env-failed":        "CREATE_FAILED",
	"env-creating":      "CREATING",
	"env-deleting":      "DELETING",
	"env-delete-failed": "DELETE_FAILED",
	"env-ssh":           "CREATED",
}

// recordCloud9Types is the type the fake Cloud9 API describes each
// environment with. An environment missing here is left out of the
// description, as one deleted after it was listed would be.
var recordCloud9Types = map[string]string{
	"env-created":       "ec2",
	"env-failed":        "ec2",
	"env-creating":      "ec2",
	"env-deleting":      "ec2",
	"env-delete-failed": "ec2",
	"env-unknown":       "ec2",
	"env-ssh":           "ssh",
}

// recordCloud9IDs returns the environment IDs a DescribeEnvironments call
// asks for.
func recordCloud9IDs(t *testing.T, call apiCall) []string {
	t.Helper()
	var body struct {
		EnvironmentIDs []string `json:"environmentIds"`
	}
	if err := json.Unmarshal([]byte(call.Body), &body); err != nil {
		t.Errorf("DescribeEnvironments body %s: %v", call.Body, err)
	}
	return body.EnvironmentIDs
}

// recordCloud9Describe answers a DescribeEnvironments call with the
// environments of types it asks for, in the order asked, each with its
// lifecycle in recordCloud9Lifecycle. It fails the test for a call asking
// for more than 25 environments, which AWS rejects.
func recordCloud9Describe(t *testing.T, call apiCall, types map[string]string) string {
	t.Helper()
	ids := recordCloud9IDs(t, call)
	if len(ids) == 0 || len(ids) > 25 {
		t.Errorf("DescribeEnvironments asked for %d environments, want 1 to 25", len(ids))
	}
	var envs []string
	for _, id := range ids {
		kind, ok := types[id]
		if !ok {
			continue
		}
		lifecycle := ""
		if status, ok := recordCloud9Lifecycle[id]; ok {
			lifecycle = fmt.Sprintf(`,"lifecycle":{"status":%q}`, status)
		}
		envs = append(envs, fmt.Sprintf(`{"id":%q,"name":%q,"type":%q,"arn":"arn:aws:cloud9:us-east-1:123456789012:environment:%s","ownerArn":"arn:aws:iam::123456789012:user/dev"%s}`,
			id, "name of "+id, kind, id, lifecycle))
	}
	return `{"environments":[` + strings.Join(envs, ",") + `]}`
}

// The lister lists every Cloud9 EC2 environment by ID, across pages, after
// describing each page's environments, without a call per environment. It
// leaves out by their lifecycle the environments that failed to be created
// and those being deleted; every other lifecycle, a failed delete
// included, and none at all is listed. An SSH environment, which Terraform
// has no type for, and one the description leaves out are skipped.
func TestRecordCloud9ListsEnvironments(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListEnvironments":
			if recordSecondPage(call) {
				return `{"environmentIds":["env-gone","env-deleting","env-delete-failed","env-unknown"]}`
			}
			return `{"environmentIds":["env-created","env-failed","env-ssh","env-creating"],"nextToken":"` + nextPageToken + `"}`
		case "DescribeEnvironments":
			return recordCloud9Describe(t, call, recordCloud9Types)
		}
		return recordUnexpected(t, call)
	})
	g := &Cloud9Lister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	env := func(id string) recordedResource {
		return recordedResource{"aws_cloud9_environment_ec2", id, id, nil}
	}
	assertRecorded(t, g.Resources,
		env("env-created"),
		env("env-creating"),
		env("env-delete-failed"),
		env("env-unknown"),
	)
}

// A page of more than 25 environments is described 25 at a time, and its
// environments are listed in the order of the page.
func TestRecordCloud9DescribesInBatches(t *testing.T) {
	var ids []string
	types := map[string]string{}
	for i := range 30 {
		id := fmt.Sprintf("env-%02d", i)
		ids = append(ids, id)
		types[id] = "ec2"
	}
	var batches []int
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListEnvironments":
			page, err := json.Marshal(map[string][]string{"environmentIds": ids})
			if err != nil {
				t.Error(err)
			}
			return string(page)
		case "DescribeEnvironments":
			batches = append(batches, len(recordCloud9IDs(t, call)))
			return recordCloud9Describe(t, call, types)
		}
		return recordUnexpected(t, call)
	})
	g := &Cloud9Lister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	if fmt.Sprint(batches) != "[25 5]" {
		t.Errorf("DescribeEnvironments batches %v, want [25 5]", batches)
	}
	var want []recordedResource
	for _, id := range ids {
		want = append(want, recordedResource{"aws_cloud9_environment_ec2", id, id, nil})
	}
	assertRecorded(t, g.Resources, want...)
}

// An error listing the environments or describing them is returned.
func TestRecordCloud9ReturnsErrors(t *testing.T) {
	for _, failing := range []string{"ListEnvironments", "DescribeEnvironments"} {
		t.Run(failing, func(t *testing.T) {
			useFakeAPI(t, func(call apiCall) string {
				switch call.Op {
				case failing:
					return recordJSONError("AccessDeniedException")
				case "ListEnvironments":
					return `{"environmentIds":["env-created"]}`
				case "DescribeEnvironments":
					return recordCloud9Describe(t, call, recordCloud9Types)
				}
				return recordUnexpected(t, call)
			})
			g := &Cloud9Lister{}

			assertRecordedError(t, g.List(), "AccessDeniedException")
		})
	}
}
