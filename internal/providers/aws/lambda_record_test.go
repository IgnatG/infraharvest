// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const recordLambdaARN = "arn:aws:lambda:us-east-1:123456789012:"

// recordLambdaPolicy is a GetPolicy answer carrying policy.
func recordLambdaPolicy(policy string) string {
	b, err := json.Marshal(map[string]string{"Policy": policy, "RevisionId": "r-1"})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// recordLambdaAPI answers the Lambda lister: functions resize and report on
// two pages, resize with a resource policy and two event invoke configs,
// two event source mappings, and layers deps (two versions) and tools.
// Answers in override win.
func recordLambdaAPI(t *testing.T, override map[string]string) func(apiCall) string {
	return func(call apiCall) string {
		path := strings.TrimSuffix(call.Path, "/")
		if answer, ok := override[path]; ok {
			return answer
		}
		switch path {
		case "/2015-03-31/functions":
			if recordSecondPage(call) {
				return `{"Functions":[{"FunctionName":"report","FunctionArn":"` + recordLambdaARN + `function:report","Runtime":"python3.13"}]}`
			}
			return `{"Functions":[{"FunctionName":"resize","FunctionArn":"` + recordLambdaARN + `function:resize","Runtime":"nodejs22.x"}],"NextMarker":"` + nextPageToken + `"}`
		case "/2015-03-31/functions/" + recordLambdaARN + "function:resize/policy":
			return recordLambdaPolicy(`{"Version":"2012-10-17","Id":"default","Statement":[
				{"Sid":"AllowS3","Effect":"Allow","Principal":{"Service":"s3.amazonaws.com"},"Action":"lambda:InvokeFunction"},
				{"Sid":"AllowSNS","Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},"Action":"lambda:InvokeFunction"}]}`)
		case "/2015-03-31/functions/" + recordLambdaARN + "function:report/policy":
			// A function without a resource policy.
			return recordJSONError("ResourceNotFoundException")
		case "/2019-09-25/functions/resize/event-invoke-config/list":
			if recordSecondPage(call) {
				return `{"FunctionEventInvokeConfigs":[{"FunctionArn":"` + recordLambdaARN + `function:resize:live","MaximumRetryAttempts":0}]}`
			}
			return `{"FunctionEventInvokeConfigs":[{"FunctionArn":"` + recordLambdaARN + `function:resize:$LATEST","MaximumRetryAttempts":1}],"NextMarker":"` + nextPageToken + `"}`
		case "/2019-09-25/functions/report/event-invoke-config/list":
			return `{"FunctionEventInvokeConfigs":[]}`
		case "/2015-03-31/event-source-mappings":
			id, token := recordPage(call, "uuid")
			return fmt.Sprintf(`{"EventSourceMappings":[{"UUID":%q,"EventSourceArn":"arn:aws:sqs:us-east-1:123456789012:%s","FunctionArn":"%sfunction:resize"}],"NextMarker":%q}`,
				id, id, recordLambdaARN, token)
		case "/2018-10-31/layers":
			if recordSecondPage(call) {
				return `{"Layers":[{"LayerName":"tools","LayerArn":"` + recordLambdaARN + `layer:tools"}]}`
			}
			return `{"Layers":[{"LayerName":"deps","LayerArn":"` + recordLambdaARN + `layer:deps"}],"NextMarker":"` + nextPageToken + `"}`
		case "/2018-10-31/layers/deps/versions":
			if recordSecondPage(call) {
				return `{"LayerVersions":[{"LayerVersionArn":"` + recordLambdaARN + `layer:deps:2","Version":2}]}`
			}
			return `{"LayerVersions":[{"LayerVersionArn":"` + recordLambdaARN + `layer:deps:1","Version":1}],"NextMarker":"` + nextPageToken + `"}`
		case "/2018-10-31/layers/tools/versions":
			return `{"LayerVersions":[{"LayerVersionArn":"` + recordLambdaARN + `layer:tools:1","Version":1}]}`
		}
		return recordUnexpected(t, call)
	}
}

// The lister lists each function by ARN, followed by a permission per
// statement of its resource policy, by <function ARN>/<statement ID>, and
// its event invoke configs, by <function name>[:<qualifier>], then the
// event source mappings, then every version of every layer.
func TestRecordLambdaListsFunctionsMappingsAndLayers(t *testing.T) {
	useFakeAPI(t, recordLambdaAPI(t, nil))
	g := &LambdaLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	resize := recordLambdaARN + "function:resize"
	assertRecorded(t, g.Resources,
		recordedResource{"aws_lambda_function", resize, "resize", map[string]string{"function_name": "resize"}},
		recordedResource{"aws_lambda_permission", resize + "/AllowS3", "AllowS3", map[string]string{"statement_id": "AllowS3", "function_name": resize}},
		recordedResource{"aws_lambda_permission", resize + "/AllowSNS", "AllowSNS", map[string]string{"statement_id": "AllowSNS", "function_name": resize}},
		recordedResource{"aws_lambda_function_event_invoke_config", "resize", "feic_" + resize + ":$LATEST", nil},
		recordedResource{"aws_lambda_function_event_invoke_config", "resize:live", "feic_" + resize + ":live", nil},
		recordedResource{"aws_lambda_function", recordLambdaARN + "function:report", "report", map[string]string{"function_name": "report"}},
		recordedResource{"aws_lambda_event_source_mapping", "uuid-1", "uuid-1", map[string]string{
			"event_source_arn": "arn:aws:sqs:us-east-1:123456789012:uuid-1", "function_name": resize}},
		recordedResource{"aws_lambda_event_source_mapping", "uuid-2", "uuid-2", map[string]string{
			"event_source_arn": "arn:aws:sqs:us-east-1:123456789012:uuid-2", "function_name": resize}},
		recordedResource{"aws_lambda_layer_version", recordLambdaARN + "layer:deps:1", "deps 1", nil},
		recordedResource{"aws_lambda_layer_version", recordLambdaARN + "layer:deps:2", "deps 2", nil},
		recordedResource{"aws_lambda_layer_version", recordLambdaARN + "layer:tools:1", "tools 1", nil},
	)
}

// A function without a resource policy is not an error (above); any other
// error, and a policy that isn't JSON, are returned.
func TestRecordLambdaReturnsErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		path, answer, want string
	}{
		"ListFunctions":                  {"/2015-03-31/functions", recordJSONError("AccessDeniedException"), "AccessDeniedException"},
		"GetPolicy":                      {"/2015-03-31/functions/" + recordLambdaARN + "function:resize/policy", recordJSONError("AccessDeniedException"), "AccessDeniedException"},
		"GetPolicy not JSON":             {"/2015-03-31/functions/" + recordLambdaARN + "function:resize/policy", recordLambdaPolicy("not json"), "invalid character"},
		"ListFunctionEventInvokeConfigs": {"/2019-09-25/functions/resize/event-invoke-config/list", recordJSONError("AccessDeniedException"), "AccessDeniedException"},
		"ListEventSourceMappings":        {"/2015-03-31/event-source-mappings", recordJSONError("AccessDeniedException"), "AccessDeniedException"},
		"ListLayers":                     {"/2018-10-31/layers", recordJSONError("AccessDeniedException"), "AccessDeniedException"},
		"ListLayerVersions":              {"/2018-10-31/layers/deps/versions", recordJSONError("AccessDeniedException"), "AccessDeniedException"},
	} {
		t.Run(name, func(t *testing.T) {
			useFakeAPI(t, recordLambdaAPI(t, map[string]string{tc.path: tc.answer}))
			g := &LambdaLister{}

			assertRecordedError(t, g.List(), tc.want)
		})
	}
}
