// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// recordDynamoDbTable answers DescribeTable for a table of no global table.
func recordDynamoDbTable(call apiCall) string {
	return `{"Table":{"TableName":"` + recordJSONField(call, "TableName") + `","TableStatus":"ACTIVE"}}`
}

// The lister lists each table by name, across pages: the next page starts
// after the last table of the one before.
func TestRecordDynamoDbListsTablesByName(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListTables":
			switch recordJSONField(call, "ExclusiveStartTableName") {
			case "":
				return `{"TableNames":["orders","sessions"],"LastEvaluatedTableName":"sessions"}`
			case "sessions":
				return `{"TableNames":["users"]}`
			}
			t.Errorf("unexpected page: %s", call.Body)
			return recordJSONError("ValidationException")
		case "DescribeTable":
			return recordDynamoDbTable(call)
		}
		return recordUnexpected(t, call)
	})
	g := &DynamoDbLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_dynamodb_table", "orders", "orders", nil},
		recordedResource{"aws_dynamodb_table", "sessions", "sessions", nil},
		recordedResource{"aws_dynamodb_table", "users", "users", nil},
	)
}

// A table of a global table records the regions of its replicas, in the
// order of their names, but for those being deleted; a table of a global
// table of 2017, which is a table of its own in each region, records none,
// and a table deleted since it was listed is left out.
func TestRecordDynamoDbRecordsReplicas(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListTables":
			return `{"TableNames":["orders","legacy","gone","users"]}`
		case "DescribeTable":
			switch recordJSONField(call, "TableName") {
			case "orders":
				return `{"Table":{"TableName":"orders","GlobalTableVersion":"2019.11.21","Replicas":[` +
					`{"RegionName":"us-west-2","ReplicaStatus":"ACTIVE"},` +
					`{"RegionName":"eu-west-1","ReplicaStatus":"ACTIVE"},` +
					`{"RegionName":"ap-south-1","ReplicaStatus":"DELETING"}]}}`
			case "legacy":
				return `{"Table":{"TableName":"legacy","GlobalTableVersion":"2017.11.29","Replicas":[{"RegionName":"eu-west-1","ReplicaStatus":"ACTIVE"}]}}`
			case "gone":
				return recordJSONError("ResourceNotFoundException")
			}
			return recordDynamoDbTable(call)
		}
		return recordUnexpected(t, call)
	})
	g := &DynamoDbLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_dynamodb_table", "orders", "orders", map[string]string{
			"replica.#":             "2",
			"replica.0.region_name": "eu-west-1",
			"replica.1.region_name": "us-west-2",
		}},
		recordedResource{"aws_dynamodb_table", "legacy", "legacy", nil},
		recordedResource{"aws_dynamodb_table", "users", "users", nil},
	)
}

// A table that can't be described is still recorded, without replicas, and
// the service fails in part.
func TestRecordDynamoDbDescribeErrorIsPartial(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListTables":
			return `{"TableNames":["orders","users"]}`
		case "DescribeTable":
			if recordJSONField(call, "TableName") == "orders" {
				return recordJSONError("AccessDeniedException")
			}
			return recordDynamoDbTable(call)
		}
		return recordUnexpected(t, call)
	})
	g := &DynamoDbLister{}

	resources, err := g.List(ctx, harvest.Settings{})

	assertRecordedError(t, err, "AccessDeniedException")
	if !harvest.IsPartial(err) {
		t.Errorf("want a partial failure, got %v", err)
	}
	assertRecorded(t, resources,
		recordedResource{"aws_dynamodb_table", "orders", "orders", nil},
		recordedResource{"aws_dynamodb_table", "users", "users", nil},
	)
}

func TestRecordDynamoDbReturnsErrors(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(apiCall) string {
		return recordJSONError("AccessDeniedException")
	})
	g := &DynamoDbLister{}

	assertRecordedError(t, listError(ctx, g), "AccessDeniedException")
}

// A global table is imported once, in the first of its regions by name:
// the default selection leaves out its replicas in the others, naming that
// region, without asking AWS more than for the stacks. A table with no
// replicas, or whose replicas are all in regions after its own, stays.
func TestExcludedByDefaultGlobalTableReplicas(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op == "ListStacks" {
			return noStacks
		}
		return recordUnexpected(t, call)
	})
	// The fake configuration lists us-east-1.
	p := &Provider{region: "us-east-1"}
	replicas := func(regions ...string) map[string]string {
		attributes := map[string]string{"replica.#": strconv.Itoa(len(regions))}
		for i, region := range regions {
			attributes["replica."+strconv.Itoa(i)+".region_name"] = region
		}
		return attributes
	}

	excluded, err := p.ExcludedByDefault(ctx, []harvest.Resource{
		harvest.Record("orders", "orders", "aws_dynamodb_table", "aws", replicas("eu-west-1", "us-west-2")),
		harvest.Record("carts", "carts", "aws_dynamodb_table", "aws", replicas("us-west-2")),
		harvest.RecordBare("users", "users", "aws_dynamodb_table", "aws"),
	})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{"aws_dynamodb_table orders": reasonGlobalTableReplica("eu-west-1")}
	if !reflect.DeepEqual(excluded, want) {
		t.Errorf("got %v, want %v", excluded, want)
	}
}

// The tables are described several at once, and recorded in the listed
// order.
func TestRecordDynamoDbDescribesTablesInParallel(t *testing.T) {
	t.Parallel()
	at := newOverlap(3)
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "ListTables":
			return `{"TableNames":["a","b","c"]}`
		case "DescribeTable":
			at.wait()
			return recordDynamoDbTable(call)
		}
		return recordUnexpected(t, call)
	})
	g := &DynamoDbLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	at.check(t)
	assertRecorded(t, resources,
		recordedResource{"aws_dynamodb_table", "a", "a", nil},
		recordedResource{"aws_dynamodb_table", "b", "b", nil},
		recordedResource{"aws_dynamodb_table", "c", "c", nil},
	)
}
