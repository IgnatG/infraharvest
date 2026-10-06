// Copyright 2026 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package newrelic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/newrelic/newrelic-client-go/v2/pkg/alerts"
)

func infraConditionsServer(t *testing.T, total int, withTotal bool) (*httptest.Server, *[]int) {
	t.Helper()
	var offsets []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Api-Key"); got != "key" {
			t.Errorf("Api-Key header %q, want %q", got, "key")
		}
		if got := r.URL.Query().Get("policy_id"); got != "7" {
			t.Errorf("policy_id %q, want 7", got)
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offsets = append(offsets, offset)
		page := infraConditionsPage{}
		for id := offset; id < total && id < offset+limit; id++ {
			page.Data = append(page.Data, alerts.InfrastructureCondition{ID: id, Name: "c", Type: "infra_metric"})
		}
		if withTotal {
			page.Meta.Total = total
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	t.Cleanup(srv.Close)
	return srv, &offsets
}

func TestListInfraConditionsPages(t *testing.T) {
	for _, withTotal := range []bool{true, false} {
		total := 2*infraConditionsPageSize + 1
		srv, offsets := infraConditionsServer(t, total, withTotal)
		got, err := listInfraConditions(context.Background(), srv.Client(), srv.URL+"/v2/alerts/conditions", "key", 7)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != total {
			t.Fatalf("withTotal=%v: got %d conditions, want %d", withTotal, len(got), total)
		}
		for i, c := range got {
			if c.ID != i {
				t.Fatalf("withTotal=%v: condition %d has ID %d", withTotal, i, c.ID)
			}
		}
		if want := []int{0, infraConditionsPageSize, 2 * infraConditionsPageSize}; len(*offsets) != len(want) {
			t.Fatalf("withTotal=%v: offsets %v, want %v", withTotal, *offsets, want)
		}
	}
}

func TestListInfraConditionsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	if _, err := listInfraConditions(context.Background(), srv.Client(), srv.URL, "key", 7); err == nil {
		t.Fatal("expected an error")
	}
}
