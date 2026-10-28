// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/api/compute/v1"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

func newSchedulerJobs(t *testing.T, region string) *SchedulerJobsLister {
	t.Helper()
	g := &SchedulerJobsLister{}
	g.Configure(harvest.ListerConfig{Provider: "google", Settings: map[string]interface{}{"project": "acme", "region": compute.Region{Name: region}}})
	g.SetContext(fastRetries(t))
	return g
}

func TestSchedulerJobsList(t *testing.T) {
	var other []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet || r.URL.Path != "/v1/projects/acme/locations/us-central1/jobs" {
			other = append(other, r.Method+" "+r.URL.Path)
			http.Error(w, `{"error": {"message": "unexpected"}}`, http.StatusNotFound)
			return
		}
		switch r.URL.Query().Get("pageToken") {
		case "":
			_, _ = w.Write([]byte(`{"jobs": [{"name": "projects/acme/locations/us-central1/jobs/nightly-export", "schedule": "0 2 * * *", "state": "ENABLED"}], "nextPageToken": "t2"}`))
		case "t2":
			_, _ = w.Write([]byte(`{"jobs": [{"name": "projects/acme/locations/us-central1/jobs/hourly-sync", "schedule": "0 * * * *", "state": "PAUSED"}]}`))
		default:
			other = append(other, "page "+r.URL.Query().Get("pageToken"))
			http.Error(w, `{"error": {"message": "bad page"}}`, http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv(EndpointEnv, server.URL)

	g := newSchedulerJobs(t, "us-central1")
	if err := g.List(); err != nil {
		t.Fatal(err)
	}
	if len(other) > 0 {
		t.Errorf("unexpected requests: %v", other)
	}

	var got []string
	for _, r := range g.Resources {
		got = append(got, r.Address.Type+" "+r.Observed.ID+" "+r.RawName)
	}
	want := []string{
		"google_cloud_scheduler_job projects/acme/locations/us-central1/jobs/nightly-export nightly-export",
		"google_cloud_scheduler_job projects/acme/locations/us-central1/jobs/hourly-sync hourly-sync",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("resources:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(g.Resources) > 0 {
		r := g.Resources[0]
		wantAttributes := map[string]string{"name": "nightly-export", "project": "acme", "region": "us-central1"}
		if !reflect.DeepEqual(r.Observed.Attributes, wantAttributes) {
			t.Errorf("attributes %v, want %v", r.Observed.Attributes, wantAttributes)
		}
		if r.Provider != "google" {
			t.Errorf("provider %q", r.Provider)
		}
	}
}

// Jobs belong to regions, so the import of global asks nothing.
func TestSchedulerJobsGlobal(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, `{"error": {"message": "unexpected"}}`, http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv(EndpointEnv, server.URL)

	g := newSchedulerJobs(t, "")
	if err := g.List(); err != nil {
		t.Fatal(err)
	}
	if len(g.Resources) != 0 || requests != 0 {
		t.Errorf("%d resources and %d requests, want none", len(g.Resources), requests)
	}
}

// A project without the API, or a region without the service, has no jobs;
// any other failure, on any page, is an error.
func TestSchedulerJobsErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  int
		body    string
		failOn  string
		wantErr bool
	}{
		{
			name:   "API not enabled",
			status: http.StatusForbidden,
			body:   `{"error": {"code": 403, "status": "PERMISSION_DENIED", "message": "Cloud Scheduler API has not been used in project acme before or it is disabled.", "details": [{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "SERVICE_DISABLED", "domain": "googleapis.com", "metadata": {"service": "cloudscheduler.googleapis.com"}}]}}`,
		},
		{
			name:   "API not enabled, older format",
			status: http.StatusForbidden,
			body:   `{"error": {"code": 403, "message": "Access Not Configured.", "errors": [{"reason": "accessNotConfigured", "message": "Access Not Configured."}]}}`,
		},
		{
			name:   "location without the service",
			status: http.StatusNotFound,
			body:   `{"error": {"code": 404, "status": "NOT_FOUND", "message": "Location not found."}}`,
		},
		{
			name:   "unknown location",
			status: http.StatusBadRequest,
			body:   `{"error": {"code": 400, "status": "INVALID_ARGUMENT", "message": "Invalid location."}}`,
		},
		{
			name:    "permission denied",
			status:  http.StatusForbidden,
			body:    `{"error": {"code": 403, "status": "PERMISSION_DENIED", "message": "The caller does not have permission", "details": [{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "IAM_PERMISSION_DENIED", "domain": "cloudscheduler.googleapis.com"}]}}`,
			wantErr: true,
		},
		{
			name:    "server error",
			status:  http.StatusInternalServerError,
			body:    `{"error": {"code": 500, "status": "INTERNAL", "message": "internal"}}`,
			wantErr: true,
		},
		{
			name:    "second page fails",
			status:  http.StatusInternalServerError,
			body:    `{"error": {"code": 500, "status": "INTERNAL", "message": "internal"}}`,
			failOn:  "t2",
			wantErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Query().Get("pageToken") == tt.failOn {
					w.WriteHeader(tt.status)
					_, _ = w.Write([]byte(tt.body))
					return
				}
				_, _ = w.Write([]byte(`{"jobs": [{"name": "projects/acme/locations/us-central1/jobs/nightly-export"}], "nextPageToken": "t2"}`))
			}))
			defer server.Close()
			t.Setenv(EndpointEnv, server.URL)

			g := newSchedulerJobs(t, "us-central1")
			err := g.List()
			if tt.wantErr {
				if err == nil {
					t.Fatal("want an error")
				}
				if !strings.Contains(err.Error(), "projects/acme/locations/us-central1") {
					t.Errorf("error %q doesn't name the location", err)
				}
			} else if err != nil {
				t.Fatalf("want no error, got %v", err)
			}
			if len(g.Resources) != 0 {
				t.Errorf("%d resources, want none", len(g.Resources))
			}
		})
	}
}
