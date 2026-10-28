// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

// The lister lists every Data Pipeline pipeline by ID, named by its name as
// AWS gives it, across pages. Names need not be unique, so two pipelines can
// share one.
func TestRecordDataPipelineListsPipelines(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "ListPipelines" {
			return recordUnexpected(t, call)
		}
		if recordJSONField(call, "marker") == nextPageToken {
			return `{"pipelineIdList":[{"id":"df-0300000000000000003","name":"nightly export"}],"hasMoreResults":false}`
		}
		return `{"pipelineIdList":[
			{"id":"df-0100000000000000001","name":"orders-etl"},
			{"id":"df-0200000000000000002","name":"orders-etl"}],
			"marker":"` + nextPageToken + `","hasMoreResults":true}`
	})
	g := &DataPipelineLister{}
	g.SetContext(ctx)

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_datapipeline_pipeline", "df-0100000000000000001", "orders-etl", nil},
		recordedResource{"aws_datapipeline_pipeline", "df-0200000000000000002", "orders-etl", nil},
		recordedResource{"aws_datapipeline_pipeline", "df-0300000000000000003", "nightly export", nil},
	)
}

func TestRecordDataPipelineReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("InvalidRequestException")
	})
	g := &DataPipelineLister{}
	g.SetContext(ctx)

	assertRecordedError(t, g.List(), "InvalidRequestException")
}
