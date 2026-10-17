// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

const recordEcsARN = "arn:aws:ecs:us-east-1:123456789012:"

// recordEcsTaskDefinition is what the lister records for the task
// definition arn.
func recordEcsTaskDefinition(family, arn string) recordedResource {
	return recordedResource{"aws_ecs_task_definition", arn, family, map[string]string{
		"task_definition":       arn,
		"container_definitions": "{}",
		"family":                family,
		"arn":                   arn,
	}}
}

// The lister lists clusters across pages, each by ARN and named after its
// name, followed by its services across pages of 100, each page described ten at a
// time for their task definitions; services whose description fails, or
// that the description leaves out, are left out, the failures returned as a
// partial error. Then the latest revision
// of each task definition family, across pages, in no particular order.
// (Errors of ListServices are not recorded: the lister retries the failed
// page for ever.)
func TestRecordEcsListsClustersServicesAndTaskDefinitions(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		cluster := recordJSONField(call, "cluster")
		switch call.Op {
		case "ListClusters":
			if recordSecondPage(call) {
				return `{"clusterArns":["` + recordEcsARN + `cluster/jobs"]}`
			}
			return fmt.Sprintf(`{"clusterArns":["%scluster/apps"],"nextToken":%q}`, recordEcsARN, nextPageToken)
		case "ListServices":
			if !strings.Contains(call.Body, `"maxResults":100`) {
				t.Errorf("services listed without pages of 100: %s", call.Body)
			}
			switch {
			case cluster == recordEcsARN+"cluster/apps" && recordSecondPage(call):
				return `{"serviceArns":["` + recordEcsARN + `service/apps/api"]}`
			case cluster == recordEcsARN+"cluster/apps":
				arns := []string{recordEcsARN + "service/apps/web", recordEcsARN + "service/apps/gone"}
				for i := range 10 {
					arns = append(arns, fmt.Sprintf("%sservice/apps/batch-%d", recordEcsARN, i))
				}
				page, _ := json.Marshal(map[string]any{"serviceArns": arns, "nextToken": nextPageToken})
				return string(page)
			case cluster == recordEcsARN+"cluster/jobs":
				return `{"serviceArns":["` + recordEcsARN + `service/jobs/worker"]}`
			}
		case "DescribeServices":
			var request struct {
				Services []string `json:"services"`
			}
			if err := json.Unmarshal([]byte(call.Body), &request); err != nil || len(request.Services) == 0 || len(request.Services) > 10 {
				t.Errorf("services described: %s", call.Body)
				return recordJSONError("InvalidParameterException")
			}
			switch first := request.Services[0]; {
			case cluster == recordEcsARN+"cluster/apps" && first == recordEcsARN+"service/apps/web":
				// The first ten of the page: only web is described, gone
				// has been deleted since it was listed, and the answer
				// leaves out batch-0 to batch-7 too. The other two fail.
				if len(request.Services) != 10 {
					t.Errorf("services described: %s", call.Body)
				}
				return `{"services":[{"serviceArn":"` + recordEcsARN + `service/apps/web","serviceName":"web","clusterArn":"` + recordEcsARN + `cluster/apps",
					"taskDefinition":"` + recordEcsARN + `task-definition/web:7","desiredCount":2,"status":"ACTIVE"}],
					"failures":[{"arn":"` + recordEcsARN + `service/apps/gone","reason":"MISSING"}]}`
			case cluster == recordEcsARN+"cluster/apps" && first == recordEcsARN+"service/apps/batch-8":
				return recordJSONError("AccessDeniedException")
			case cluster == recordEcsARN+"cluster/apps" && first == recordEcsARN+"service/apps/api":
				return `{"services":[{"serviceArn":"` + recordEcsARN + `service/apps/api","serviceName":"api","clusterArn":"` + recordEcsARN + `cluster/apps",
					"taskDefinition":"` + recordEcsARN + `task-definition/api:3","desiredCount":1,"status":"ACTIVE"}],"failures":[]}`
			case cluster == recordEcsARN+"cluster/jobs" && first == recordEcsARN+"service/jobs/worker":
				return recordJSONError("AccessDeniedException")
			}
		case "ListTaskDefinitions":
			if recordSecondPage(call) {
				return `{"taskDefinitionArns":["` + recordEcsARN + `task-definition/web:5","` + recordEcsARN + `task-definition/api:3","` +
					recordEcsARN + `task-definition/worker:3"]}`
			}
			return fmt.Sprintf(`{"taskDefinitionArns":["%[1]stask-definition/web:6","%[1]stask-definition/web:7","%[1]stask-definition/worker:2"],
				"nextToken":%[2]q}`, recordEcsARN, nextPageToken)
		}
		return recordUnexpected(t, call)
	})
	g := &EcsLister{}

	// The descriptions that fail are returned as a partial error.
	if err := g.List(); !harvest.IsPartial(err) || !strings.Contains(err.Error(), "service/apps/batch-8") || !strings.Contains(err.Error(), "service/jobs/worker") {
		t.Errorf("want a partial error naming the services not described, got %v", err)
	}

	// Task definitions come last, in no particular order: sort them.
	resources := slices.Clone(g.Resources)
	first := slices.IndexFunc(resources, func(r harvest.Resource) bool { return r.Address.Type == "aws_ecs_task_definition" })
	if first >= 0 {
		slices.SortFunc(resources[first:], func(a, b harvest.Resource) int { return strings.Compare(a.Observed.ID, b.Observed.ID) })
	}
	assertRecorded(t, resources,
		recordedResource{"aws_ecs_cluster", recordEcsARN + "cluster/apps", "apps", nil},
		recordedResource{"aws_ecs_service", recordEcsARN + "service/apps/web", "apps_web", map[string]string{
			"task_definition": recordEcsARN + "task-definition/web:7",
			"cluster":         "apps",
			"name":            "web",
			"id":              recordEcsARN + "service/apps/web",
		}},
		recordedResource{"aws_ecs_service", recordEcsARN + "service/apps/api", "apps_api", map[string]string{
			"task_definition": recordEcsARN + "task-definition/api:3",
			"cluster":         "apps",
			"name":            "api",
			"id":              recordEcsARN + "service/apps/api",
		}},
		recordedResource{"aws_ecs_cluster", recordEcsARN + "cluster/jobs", "jobs", nil},
		recordEcsTaskDefinition("api", recordEcsARN+"task-definition/api:3"),
		recordEcsTaskDefinition("web", recordEcsARN+"task-definition/web:7"),
		recordEcsTaskDefinition("worker", recordEcsARN+"task-definition/worker:3"),
	)
}

// Listing clusters or task definitions fails the service.
func TestRecordEcsReturnsErrors(t *testing.T) {
	for _, failing := range []string{"ListClusters", "ListTaskDefinitions"} {
		t.Run(failing, func(t *testing.T) {
			useFakeAPI(t, func(call apiCall) string {
				switch call.Op {
				case failing:
					return recordJSONError("AccessDeniedException")
				case "ListClusters":
					return `{"clusterArns":[]}`
				}
				return recordUnexpected(t, call)
			})
			g := &EcsLister{}

			assertRecordedError(t, g.List(), "AccessDeniedException")
		})
	}
}
