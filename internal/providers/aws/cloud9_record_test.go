// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// recordCloud9Status is the status the fake Cloud9 API gives each
// environment. An environment missing here is not described by the fake.
var recordCloud9Status = map[string]string{
	"env-ready":      `{"status":"ready","message":"recorded"}`,
	"env-error":      `{"status":"error","message":"recorded"}`,
	"env-creating":   `{"status":"creating","message":"recorded"}`,
	"env-connecting": `{"status":"connecting","message":"recorded"}`,
	"env-stopping":   `{"status":"stopping","message":"recorded"}`,
	"env-stopped":    `{"status":"stopped","message":"recorded"}`,
	"env-deleting":   `{"status":"deleting","message":"recorded"}`,
	"env-unknown":    `{}`,
}

// recordCloud9Types is the type the fake Cloud9 API describes each
// environment with. An environment missing here is left out of the
// description, as one deleted after it was listed would be.
var recordCloud9Types = map[string]string{
	"env-ready":      "ec2",
	"env-error":      "ec2",
	"env-creating":   "ec2",
	"env-connecting": "ec2",
	"env-stopping":   "ec2",
	"env-stopped":    "ec2",
	"env-deleting":   "ec2",
	"env-unknown":    "ec2",
	"env-ssh":        "ssh",
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
// environments of types it asks for, in the order asked. It fails the test
// for a call asking for more than 25 environments, which AWS rejects.
func recordCloud9Describe(t *testing.T, call apiCall, types map[string]string) string {
	t.Helper()
	ids := recordCloud9IDs(t, call)
	if len(ids) == 0 || len(ids) > 25 {
		t.Errorf("DescribeEnvironments asked for %d environments, want 1 to 25", len(ids))
	}
	var envs []string
	for _, id := range ids {
		if kind, ok := types[id]; ok {
			envs = append(envs, fmt.Sprintf(`{"id":%q,"name":%q,"type":%q,"arn":"arn:aws:cloud9:us-east-1:123456789012:environment:%s","ownerArn":"arn:aws:iam::123456789012:user/dev"}`, id, "name of "+id, kind, id))
		}
	}
	return `{"environments":[` + strings.Join(envs, ",") + `]}`
}

// The lister lists every Cloud9 EC2 environment by ID, across pages, after
// describing each page's environments and asking each EC2 one's status. It
// leaves out the environments in error and those being deleted; every
// other status, and no status at all, is listed. An SSH environment, which
// Terraform has no type for, and one the description leaves out are
// skipped without asking their status: the fake API has none for them.
func TestRecordCloud9ListsEnvironments(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListEnvironments":
			if recordSecondPage(call) {
				return `{"environmentIds":["env-stopping","env-stopped","env-gone","env-deleting","env-unknown"]}`
			}
			return `{"environmentIds":["env-ready","env-error","env-ssh","env-creating","env-connecting"],"nextToken":"` + nextPageToken + `"}`
		case "DescribeEnvironments":
			return recordCloud9Describe(t, call, recordCloud9Types)
		case "DescribeEnvironmentStatus":
			id := recordJSONField(call, "environmentId")
			if status, ok := recordCloud9Status[id]; ok && recordCloud9Types[id] == "ec2" {
				return status
			}
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
		env("env-ready"),
		env("env-creating"),
		env("env-connecting"),
		env("env-stopping"),
		env("env-stopped"),
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
		case "DescribeEnvironmentStatus":
			return `{"status":"ready","message":"recorded"}`
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

// An error listing the environments, describing them or asking one's
// status is returned.
func TestRecordCloud9ReturnsErrors(t *testing.T) {
	for _, failing := range []string{"ListEnvironments", "DescribeEnvironments", "DescribeEnvironmentStatus"} {
		t.Run(failing, func(t *testing.T) {
			useFakeAPI(t, func(call apiCall) string {
				switch call.Op {
				case failing:
					return recordJSONError("AccessDeniedException")
				case "ListEnvironments":
					return `{"environmentIds":["env-ready"]}`
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
