// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

const recordCodePipelineWebhook = "arn:aws:codepipeline:us-east-1:123456789012:webhook:"

// The lister lists pipelines by name, then webhooks by ARN, each across
// pages.
func TestRecordCodePipelineListsPipelinesAndWebhooks(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListPipelines":
			if recordSecondPage(call) {
				return `{"pipelines":[{"name":"api","version":1,"pipelineType":"V2"}]}`
			}
			return fmt.Sprintf(`{"pipelines":[{"name":"web","version":4,"pipelineType":"V1"}],"nextToken":%q}`, nextPageToken)
		case "ListWebhooks":
			if recordSecondPage(call) {
				return `{"webhooks":[{"arn":"` + recordCodePipelineWebhook + `api-push","url":"https://hooks.example/2"}]}`
			}
			return fmt.Sprintf(`{"webhooks":[{"arn":"%sweb-push","url":"https://hooks.example/1"}],"NextToken":%q}`,
				recordCodePipelineWebhook, nextPageToken)
		}
		return recordUnexpected(t, call)
	})
	g := &CodePipelineLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_codepipeline", "web", "web", nil},
		recordedResource{"aws_codepipeline", "api", "api", nil},
		recordedResource{"aws_codepipeline_webhook", recordCodePipelineWebhook + "web-push", "web-push", nil},
		recordedResource{"aws_codepipeline_webhook", recordCodePipelineWebhook + "api-push", "api-push", nil},
	)
}

// Each list fails the service.
func TestRecordCodePipelineReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, failing := range []string{"ListPipelines", "ListWebhooks"} {
		t.Run(failing, func(t *testing.T) {
			ctx := useFakeAPI(t, func(call apiCall) string {
				switch call.Op {
				case failing:
					return recordJSONError("AccessDeniedException")
				case "ListPipelines":
					return `{"pipelines":[]}`
				}
				return recordUnexpected(t, call)
			})
			g := &CodePipelineLister{}

			assertRecordedError(t, listError(ctx, g), "AccessDeniedException")
		})
	}
}
