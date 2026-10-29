// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"

	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

// fastRetries returns a context of a run of its own whose retries wait a
// millisecond (see runRetryDelay), so that the tests of failing calls don't
// wait for Google's backoff.
func fastRetries(t *testing.T) context.Context {
	return fastRetriesIn(t.Context())
}

// fastRetriesIn is fastRetries, with a context made from ctx.
func fastRetriesIn(ctx context.Context) context.Context {
	run := ratelimit.Like(ratelimit.Default())
	ratelimit.Kept(run, retryDelayKey{}, func() time.Duration { return time.Millisecond })
	return ratelimit.NewContext(ctx, run)
}

// failingServer answers each request with the next of statuses, then
// with a network list, counting the requests and the bodies they carry.
type failingServer struct {
	statuses   []int
	retryAfter string

	mu       sync.Mutex
	requests int
	bodies   []string
}

func (f *failingServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	n := f.requests
	f.requests++
	f.bodies = append(f.bodies, string(body))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if n < len(f.statuses) {
		if f.retryAfter != "" {
			w.Header().Set("Retry-After", f.retryAfter)
		}
		w.WriteHeader(f.statuses[n])
		_, _ = fmt.Fprintf(w, `{"error":{"code":%d,"message":"failed"}}`, f.statuses[n])
		return
	}
	_, _ = w.Write([]byte(`{"items":[{"name":"main"}]}`))
}

func (f *failingServer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

// networks lists the networks of a project from server, through the
// clients the listers use.
func networks(t *testing.T, server *httptest.Server) (*compute.NetworkList, error) {
	t.Helper()
	t.Setenv(EndpointEnv, server.URL)
	svc, err := compute.NewService(t.Context(), clientOptions(fastRetries(t))...)
	if err != nil {
		t.Fatal(err)
	}
	return svc.Networks.List("acme-prod").Context(t.Context()).Do()
}

// A throttled or failing call is sent again until it succeeds.
func TestRetriesThrottledAndFailedCalls(t *testing.T) {
	f := &failingServer{statuses: []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusInternalServerError}}
	server := httptest.NewServer(f)
	defer server.Close()
	list, err := networks(t, server)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || f.count() != 4 {
		t.Errorf("got %+v after %d requests, want the list after 4", list.Items, f.count())
	}
}

// A call that keeps failing is sent retryAttempts times, then fails with
// the last answer.
func TestRetriesStopAfterTheLastAttempt(t *testing.T) {
	statuses := make([]int, retryAttempts+1)
	for i := range statuses {
		statuses[i] = http.StatusBadGateway
	}
	f := &failingServer{statuses: statuses}
	server := httptest.NewServer(f)
	defer server.Close()
	if _, err := networks(t, server); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("error %v, want the 502", err)
	}
	if f.count() != retryAttempts {
		t.Errorf("got %d requests, want %d", f.count(), retryAttempts)
	}
}

// An answer that can't pass, such as a 403 or a 404, isn't sent again.
func TestNoRetryForClientErrors(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound} {
		f := &failingServer{statuses: []int{status}}
		server := httptest.NewServer(f)
		if _, err := networks(t, server); err == nil {
			t.Errorf("%d: want an error", status)
		}
		if f.count() != 1 {
			t.Errorf("%d: got %d requests, want 1", status, f.count())
		}
		server.Close()
	}
}

// Compute Engine, Cloud DNS, the SQL Admin API and Cloud Resource Manager
// v1 throttle with a 403 whose reason says so: that is sent again, as a
// 429 is. Any other 403, as a permission denied or a quota spent for the
// day, fails at once, with its whole error.
func TestRetriesRateLimited403(t *testing.T) {
	for _, tt := range []struct {
		name  string
		body  string
		retry bool
	}{
		{"rateLimitExceeded", `{"error": {"code": 403, "message": "Rate Limit Exceeded", "errors": [{"domain": "usageLimits", "reason": "rateLimitExceeded", "message": "Rate Limit Exceeded"}]}}`, true},
		{"userRateLimitExceeded", `{"error": {"code": 403, "message": "User Rate Limit Exceeded", "errors": [{"domain": "usageLimits", "reason": "userRateLimitExceeded"}]}}`, true},
		{"ErrorInfo RATE_LIMIT_EXCEEDED", `{"error": {"code": 403, "message": "Quota exceeded for quota metric 'Read requests'.", "status": "PERMISSION_DENIED", "details": [{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "RATE_LIMIT_EXCEEDED", "domain": "googleapis.com"}]}}`, true},
		{"permission denied", `{"error": {"code": 403, "message": "Required 'compute.networks.list' permission for 'projects/acme-prod'", "errors": [{"domain": "global", "reason": "forbidden"}]}}`, false},
		{"IAM_PERMISSION_DENIED", `{"error": {"code": 403, "message": "Permission denied on resource project acme-prod.", "status": "PERMISSION_DENIED", "details": [{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "IAM_PERMISSION_DENIED", "domain": "iam.googleapis.com"}]}}`, false},
		{"daily quota", `{"error": {"code": 403, "message": "Quota exceeded", "errors": [{"domain": "usageLimits", "reason": "quotaExceeded"}]}}`, false},
		{"not JSON", `Forbidden`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				n := requests
				requests++
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if n == 0 {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(tt.body))
					return
				}
				_, _ = w.Write([]byte(`{"items":[{"name":"main"}]}`))
			}))
			defer server.Close()

			list, err := networks(t, server)
			mu.Lock()
			made := requests
			mu.Unlock()
			if tt.retry {
				if err != nil || len(list.Items) != 1 || made != 2 {
					t.Errorf("got %v after %d requests, want the list after 2", err, made)
				}
				return
			}
			if made != 1 {
				t.Errorf("got %d requests, want 1", made)
			}
			// The client still reads the whole error the retry looked
			// into.
			var apiErr *googleapi.Error
			if !errors.As(err, &apiErr) || apiErr.Code != http.StatusForbidden || apiErr.Body != tt.body {
				t.Errorf("error %#v, want the 403 with its body", err)
			}
		})
	}
}

// A Retry-After longer than retryMaxDelay isn't waited for: the 429 is
// returned.
func TestNoRetryForALongRetryAfter(t *testing.T) {
	f := &failingServer{statuses: []int{http.StatusTooManyRequests}, retryAfter: "3600"}
	server := httptest.NewServer(f)
	defer server.Close()
	if _, err := networks(t, server); err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("error %v, want the 429", err)
	}
	if f.count() != 1 {
		t.Errorf("got %d requests, want 1", f.count())
	}
}

// A request's body is sent again whole with each retry.
func TestRetrySendsTheBodyAgain(t *testing.T) {
	t.Parallel()
	f := &failingServer{statuses: []int{http.StatusServiceUnavailable}}
	server := httptest.NewServer(f)
	defer server.Close()
	client := &http.Client{Transport: retryTransport{base: http.DefaultTransport, delay: time.Millisecond}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, strings.NewReader(`{"options":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(f.bodies) != 2 || f.bodies[0] != f.bodies[1] || f.bodies[1] != `{"options":{}}` {
		t.Errorf("status %d, bodies %q", resp.StatusCode, f.bodies)
	}
}

// A retry stops waiting when the request's context ends.
func TestRetryStopsWithTheContext(t *testing.T) {
	t.Parallel()
	f := &failingServer{statuses: []int{http.StatusServiceUnavailable}}
	server := httptest.NewServer(f)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	client := &http.Client{Transport: retryTransport{base: http.DefaultTransport, delay: time.Minute}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("want the context's error")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("waited %s", elapsed)
	}
}

func TestRetryAfter(t *testing.T) {
	t.Parallel()
	for header, want := range map[string]time.Duration{
		"":      -1,
		"2":     2 * time.Second,
		"0":     0,
		"later": -1,
		"-1":    -1,
	} {
		got, ok := retryAfter(header)
		if !ok {
			got = -1
		}
		if got != want {
			t.Errorf("%q: got %s, want %s", header, got, want)
		}
	}
	if got, ok := retryAfter(time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)); !ok || got != 0 {
		t.Errorf("a date gone: got %s, %v", got, ok)
	}
}
