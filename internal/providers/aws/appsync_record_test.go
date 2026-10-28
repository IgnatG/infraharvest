// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The lister lists GraphQL APIs by ID, named after their names, across
// pages.
func TestRecordAppSyncListsGraphqlApis(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Path != "/v1/apis" {
			return recordUnexpected(t, call)
		}
		if recordSecondPage(call) {
			return `{"graphqlApis":[{"apiId":"api2","name":"inventory","authenticationType":"AWS_IAM"}]}`
		}
		return `{"graphqlApis":[{"apiId":"api1","name":"orders","authenticationType":"API_KEY",
			"arn":"arn:aws:appsync:us-east-1:123456789012:apis/api1"}],"nextToken":"` + nextPageToken + `"}`
	})
	g := &AppSyncLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_appsync_graphql_api", "api1", "orders", nil},
		recordedResource{"aws_appsync_graphql_api", "api2", "inventory", nil},
	)
}

func TestRecordAppSyncReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("UnauthorizedException")
	})
	g := &AppSyncLister{}

	assertRecordedError(t, listError(ctx, g), "UnauthorizedException")
}
