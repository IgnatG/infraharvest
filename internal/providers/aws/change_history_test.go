// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// lookupEventsRequest is what LastChanges asks of LookupEvents.
type lookupEventsRequest struct {
	LookupAttributes []struct{ AttributeKey, AttributeValue string }
	StartTime        float64
	MaxResults       int
	NextToken        string
}

// LastChanges takes, for each ID, the newest event that isn't read-only,
// reading on through pages of read-only ones; a service's own call is
// told by its source; an ID with no change is left out.
func TestLastChanges(t *testing.T) {
	t.Parallel()
	before := time.Now()
	ctx := useFakeAPI(t, func(call apiCall) string {
		if call.Op != "LookupEvents" {
			return recordUnexpected(t, call)
		}
		var req lookupEventsRequest
		if err := json.Unmarshal([]byte(call.Body), &req); err != nil {
			t.Fatal(err)
		}
		if len(req.LookupAttributes) != 1 || req.LookupAttributes[0].AttributeKey != "ResourceName" || req.MaxResults != 50 {
			t.Errorf("request: %s", call.Body)
		}
		if start := time.Unix(int64(req.StartTime), 0); before.Sub(start) < 89*24*time.Hour || before.Sub(start) > 91*24*time.Hour {
			t.Errorf("StartTime %s: want 90 days back", start)
		}
		switch id := req.LookupAttributes[0].AttributeValue; {
		case id == "sg-1" && req.NextToken == "":
			return `{"Events":[{"EventName":"DescribeSecurityGroups","ReadOnly":"true","Username":"reader","EventTime":1762600000}],"NextToken":"` + nextPageToken + `"}`
		case id == "sg-1":
			return `{"Events":[` +
				`{"EventName":"AuthorizeSecurityGroupIngress","ReadOnly":"false","Username":"alice","EventTime":1762500000},` +
				`{"EventName":"CreateSecurityGroup","ReadOnly":"false","Username":"bob","EventTime":1762400000}]}`
		case id == "i-1":
			return `{"Events":[{"EventName":"StopInstances","ReadOnly":"false","EventSource":"autoscaling.amazonaws.com","EventTime":1762300000}]}`
		default:
			return `{"Events":[{"EventName":"DescribeInstances","ReadOnly":"true","Username":"reader","EventTime":1762600000}]}`
		}
	})
	p := &Provider{}

	found, err := p.LastChanges(ctx, map[string]string{"region": "eu-west-2"}, []string{"sg-1", "i-1", "vpc-quiet"})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]harvest.ChangeRecord{
		"sg-1": {By: "alice", Call: "AuthorizeSecurityGroupIngress", At: time.Unix(1762500000, 0), Log: "CloudTrail"},
		"i-1":  {By: "autoscaling.amazonaws.com", Call: "StopInstances", At: time.Unix(1762300000, 0), Log: "CloudTrail"},
	}
	if len(found) != len(want) {
		t.Fatalf("found %v, want %v", found, want)
	}
	for id, w := range want {
		got := found[id]
		if got.By != w.By || got.Call != w.Call || !got.At.Equal(w.At) || got.Log != w.Log {
			t.Errorf("%s: got %+v, want %+v", id, got, w)
		}
	}
}

// Without a region, nothing is asked; a failed lookup returns what was
// found before it, with the error.
func TestLastChangesFailures(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		var req lookupEventsRequest
		_ = json.Unmarshal([]byte(call.Body), &req)
		if req.LookupAttributes[0].AttributeValue == "denied" {
			return recordJSONError("AccessDeniedException")
		}
		return `{"Events":[{"EventName":"PutBucketTagging","ReadOnly":"false","Username":"alice","EventTime":1762500000}]}`
	})
	p := &Provider{}

	if _, err := p.LastChanges(ctx, nil, []string{"bucket"}); err == nil || !strings.Contains(err.Error(), "no region") {
		t.Errorf("without a region: %v", err)
	}
	found, err := p.LastChanges(ctx, map[string]string{"region": "eu-west-2"}, []string{"bucket", "denied", "after"})
	if err == nil || !strings.Contains(err.Error(), "cloudtrail:LookupEvents in eu-west-2") {
		t.Errorf("err: %v", err)
	}
	if _, ok := found["bucket"]; !ok || len(found) != 1 {
		t.Errorf("found %v: want bucket's alone", found)
	}
}

// The AWS provider reads CloudTrail for drift.
var _ harvest.ProviderWithChangeHistory = (*Provider)(nil)
