// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

const recordBatchARN = "arn:aws:batch:us-east-1:123456789012:"

func recordBatchAPI(t *testing.T, failing string) func(apiCall) string {
	return func(call apiCall) string {
		if call.Path == failing {
			return recordJSONError("ClientException")
		}
		second := recordSecondPage(call)
		next := `,"nextToken":"` + nextPageToken + `"`
		switch call.Path {
		case "/v1/describecomputeenvironments":
			if second {
				return `{"computeEnvironments":[{"computeEnvironmentName":"ondemand","computeEnvironmentArn":"` + recordBatchARN + `compute-environment/ondemand","state":"ENABLED","status":"VALID"},
					{"computeEnvironmentName":"retired","computeEnvironmentArn":"` + recordBatchARN + `compute-environment/retired","state":"DISABLED","status":"DELETING"}]}`
			}
			return `{"computeEnvironments":[{"computeEnvironmentName":"spot","computeEnvironmentArn":"` + recordBatchARN + `compute-environment/spot","state":"ENABLED","status":"VALID"}]` + next + `}`
		case "/v1/describejobdefinitions":
			if status := recordJSONField(call, "status"); status != "ACTIVE" {
				t.Errorf("job definitions of status %q", status)
			}
			if second {
				return `{"jobDefinitions":[{"jobDefinitionName":"encode","jobDefinitionArn":"` + recordBatchARN + `job-definition/encode:1","revision":1,"status":"ACTIVE","type":"container"}]}`
			}
			return `{"jobDefinitions":[{"jobDefinitionName":"render","jobDefinitionArn":"` + recordBatchARN + `job-definition/render:3","revision":3,"status":"ACTIVE","type":"container"}]` + next + `}`
		case "/v1/describejobqueues":
			if second {
				return `{"jobQueues":[{"jobQueueName":"low","jobQueueArn":"` + recordBatchARN + `job-queue/low","state":"ENABLED","status":"VALID","priority":1},
					{"jobQueueName":"old","jobQueueArn":"` + recordBatchARN + `job-queue/old","state":"DISABLED","status":"DELETED","priority":1}]}`
			}
			return `{"jobQueues":[{"jobQueueName":"high","jobQueueArn":"` + recordBatchARN + `job-queue/high","state":"ENABLED","priority":10}]` + next + `}`
		}
		return recordUnexpected(t, call)
	}
}

// The lister lists compute environments by name, active job definitions by
// ARN, named <name>:<revision>, and job queues by ARN, each across pages,
// all by the ID Terraform imports them by. Environments and queues being
// deleted, or deleted, are left out.
func TestRecordBatchListsEnvironmentsDefinitionsAndQueues(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, recordBatchAPI(t, ""))
	g := &BatchLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_batch_compute_environment", "spot", "spot", map[string]string{"compute_environment_name": "spot"}},
		recordedResource{"aws_batch_compute_environment", "ondemand", "ondemand", map[string]string{"compute_environment_name": "ondemand"}},
		recordedResource{"aws_batch_job_definition", recordBatchARN + "job-definition/render:3", "render:3", map[string]string{"arn": recordBatchARN + "job-definition/render:3"}},
		recordedResource{"aws_batch_job_definition", recordBatchARN + "job-definition/encode:1", "encode:1", map[string]string{"arn": recordBatchARN + "job-definition/encode:1"}},
		recordedResource{"aws_batch_job_queue", recordBatchARN + "job-queue/high", "high", nil},
		recordedResource{"aws_batch_job_queue", recordBatchARN + "job-queue/low", "low", nil},
	)
}

func TestRecordBatchReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"/v1/describecomputeenvironments", "/v1/describejobdefinitions", "/v1/describejobqueues"} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, recordBatchAPI(t, failing))
			g := &BatchLister{}

			_, err := g.List(ctx, harvest.Settings{})

			assertRecordedError(t, err, "ClientException")
		})
	}
}
