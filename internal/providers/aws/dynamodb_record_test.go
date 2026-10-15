// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import "testing"

// The lister lists each table by name, across pages: the next page starts
// after the last table of the one before.
func TestRecordDynamoDbListsTablesByName(t *testing.T) {
	useFakeAPI(t, func(call apiCall) string {
		if call.Op != "ListTables" {
			return recordUnexpected(t, call)
		}
		switch recordJSONField(call, "ExclusiveStartTableName") {
		case "":
			return `{"TableNames":["orders","sessions"],"LastEvaluatedTableName":"sessions"}`
		case "sessions":
			return `{"TableNames":["users"]}`
		}
		t.Errorf("unexpected page: %s", call.Body)
		return recordJSONError("ValidationException")
	})
	g := &DynamoDbLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_dynamodb_table", "orders", "orders", nil},
		recordedResource{"aws_dynamodb_table", "sessions", "sessions", nil},
		recordedResource{"aws_dynamodb_table", "users", "users", nil},
	)
}

func TestRecordDynamoDbReturnsErrors(t *testing.T) {
	useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDeniedException")
	})
	g := &DynamoDbLister{}

	assertRecordedError(t, g.List(), "AccessDeniedException")
}
