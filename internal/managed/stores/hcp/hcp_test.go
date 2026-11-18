// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package hcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	svchost "github.com/hashicorp/terraform-svchost"

	"github.com/IgnatG/infraharvest/internal/managed"
)

func TestStore(t *testing.T) {
	const token = "api-token"
	state := `{"version": 4, "resources": []}`
	// State downloads are signed URLs on another host, which must not get
	// the token.
	archivist := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("the token went to the download host")
		}
		_, _ = w.Write([]byte(state))
	}))
	defer archivist.Close()

	names := []string{"network-prod", "network-stage", "app-network-x"}
	var lookups atomic.Int32
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body any
		switch path := r.URL.Path; {
		case path == "/api/v2/organizations/acme/workspaces":
			// One workspace a page. The search matches anywhere in the name,
			// as the API's does: app-network-x is left to List to drop.
			page, _ := strconv.Atoi(r.URL.Query().Get("page[number]"))
			var data []any
			if page <= len(names) && strings.Contains(names[page-1], r.URL.Query().Get("search[name]")) {
				data = append(data, map[string]any{"id": "ws-" + names[page-1], "attributes": map[string]any{"name": names[page-1]}})
			}
			next := page + 1
			if next > len(names) {
				next = 0
			}
			body = map[string]any{"data": data, "meta": map[string]any{"pagination": map[string]any{"next-page": next}}}
		case strings.HasPrefix(path, "/api/v2/organizations/acme/workspaces/"):
			lookups.Add(1)
			body = map[string]any{"data": map[string]any{"id": "ws-" + strings.TrimPrefix(path, "/api/v2/organizations/acme/workspaces/")}}
		case path == "/api/v2/workspaces/ws-network-prod/current-state-version":
			body = map[string]any{"data": map[string]any{"attributes": map[string]any{"hosted-state-download-url": archivist.URL + "/v1/object/signed"}}}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer api.Close()
	h := &Store{client: api.Client(), base: api.URL + "/api/v2", token: token}

	got, err := h.List(t.Context(), "acme", "network-")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "network-prod,network-stage" {
		t.Errorf("List: %v", got)
	}
	content, err := h.Get(t.Context(), "acme", "network-prod")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != state {
		t.Errorf("Get: %q", content)
	}
	// No current state version yet.
	if content, err := h.Get(t.Context(), "acme", "network-stage"); err != nil || content != nil {
		t.Errorf("Get without state: %q, %v", content, err)
	}
	// List found their IDs; a workspace it didn't list is looked up.
	if lookups.Load() != 0 {
		t.Errorf("Get looked up %d workspaces List found", lookups.Load())
	}
	fresh := &Store{client: api.Client(), base: api.URL + "/api/v2", token: token}
	if content, err := fresh.Get(t.Context(), "acme", "network-prod"); err != nil || string(content) != state || lookups.Load() != 1 {
		t.Errorf("Get of a workspace not listed: %q, %v, %d lookups", content, err, lookups.Load())
	}
	h.token = "wrong"
	if _, err := h.List(t.Context(), "acme", ""); err == nil {
		t.Error("want an error for a rejected token")
	}
}

func TestAPIToken(t *testing.T) {
	dir := t.TempDir()
	// terraform login's file: under %APPDATA% on Windows, the home
	// directory elsewhere.
	credentials := filepath.Join(dir, ".terraform.d", "credentials.tfrc.json")
	if runtime.GOOS == "windows" {
		credentials = filepath.Join(dir, "terraform.d", "credentials.tfrc.json")
	}
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Dir(credentials), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentials, []byte(`{"credentials": {"App.Terraform.io": {"token": "from-login"}, "tfe.other_example": {"token": "invalid-key"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "APPDATA" {
			return dir
		}
		return ""
	}
	// Terraform decodes each TF_TOKEN_ name, __ as - then _ as ., and
	// normalises the host: in any case, the last one set wins.
	environ := []string{"PATH=/usr/bin", "TF_TOKEN_tfe_my__corp_example=from-env", "TF_TOKEN_TFE_LOUD_EXAMPLE=old", "TF_TOKEN_tfe_loud_example=from-env-loud"}

	for host, want := range map[string]string{
		"app.terraform.io":    "from-login",
		"tfe.my-corp.example": "from-env",
		"tfe.loud.example":    "from-env-loud",
	} {
		hostname, err := svchost.ForComparison(host)
		if err != nil {
			t.Fatal(err)
		}
		got, err := apiToken(hostname, environ, getenv)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if got != want {
			t.Errorf("%s: token %q, want %q", host, got, want)
		}
	}
	if _, err := apiToken("tfe.other.example", environ, getenv); err == nil || !strings.Contains(err.Error(), "TF_TOKEN_tfe_other_example") {
		t.Errorf("want an error naming TF_TOKEN_tfe_other_example, got %v", err)
	}
}

// A host is one Terraform takes, normalised as Terraform does: one that
// isn't, such as app_terraform_io, which TF_TOKEN_app_terraform_io would
// otherwise match, gets no token.
func TestNewHost(t *testing.T) {
	t.Setenv("TF_TOKEN_app_terraform_io", "secret")
	for _, host := range []string{"app_terraform_io", "app.terraform.io/x", "https://app.terraform.io", "app.terraform.io:x", ""} {
		if _, err := New(t.Context(), host); err == nil {
			t.Errorf("%q: want an error", host)
		}
	}
	store, err := New(t.Context(), "App.Terraform.IO:443")
	if err != nil {
		t.Fatal(err)
	}
	if s := store.(*Store); s.base != "https://app.terraform.io/api/v2" || s.token != "secret" {
		t.Errorf("base %q, token %q", s.base, s.token)
	}
}

// The token goes to the API's host over https only, on a redirect too.
func TestSendsToken(t *testing.T) {
	const base = "https://tfe.example/api/v2"
	for u, want := range map[string]bool{
		"https://tfe.example/api/v2/x":     true,
		"http://tfe.example/api/v2/x":      false,
		"https://sub.tfe.example/api/v2/x": false,
		"https://tfe.example:8443/x":       false,
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, u, http.NoBody)
		if got := sendsToken(base, req.URL); got != want {
			t.Errorf("%s: sendsToken %v, want %v", u, got, want)
		}
		req.Header.Set("Authorization", "Bearer x")
		if err := tokenOnlyTo(base)(req, []*http.Request{req}); err != nil {
			t.Fatal(err)
		}
		if got := req.Header.Get("Authorization") != ""; got != want {
			t.Errorf("%s: redirect keeps the token %v, want %v", u, got, want)
		}
	}
}

// A state download URL is signed: it grants access to the state, secrets
// included, so no error may quote it, whether the download fails with a
// status or before any.
func TestDownloadErrorsHideTheURL(t *testing.T) {
	const signed = "/v1/object/signed-dG9rZW4"
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close() // nothing listens: the request fails before a status

	for name, download := range map[string]string{"status": failing.URL + signed, "transport": closed.URL + signed} {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body any
			switch r.URL.Path {
			case "/api/v2/organizations/acme/workspaces/prod":
				body = map[string]any{"data": map[string]any{"id": "ws-prod"}}
			case "/api/v2/workspaces/ws-prod/current-state-version":
				body = map[string]any{"data": map[string]any{"attributes": map[string]any{"hosted-state-download-url": download}}}
			default:
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(body)
		}))
		h := &Store{client: api.Client(), base: api.URL + "/api/v2", token: "api-token", retryBase: time.Millisecond}
		_, err := h.Get(t.Context(), "acme", "prod")
		api.Close()
		if err == nil {
			t.Errorf("%s: want an error", name)
			continue
		}
		if strings.Contains(err.Error(), "signed-") {
			t.Errorf("%s: the error quotes the signed URL: %v", name, err)
		}
		if !strings.Contains(err.Error(), "acme/prod") {
			t.Errorf("%s: the error doesn't name the workspace: %v", name, err)
		}
	}
}

// A request the API throttles or fails is asked again, with the wait its
// Retry-After asks for; one it refuses isn't, and the retries stop at
// fetchAttempts.
func TestFetchRetries(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		statuses []int
		wantErr  bool
		requests int32
	}{
		"throttled once": {statuses: []int{http.StatusTooManyRequests, http.StatusOK}, requests: 2},
		"failed twice":   {statuses: []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusOK}, requests: 3},
		"failing":        {statuses: []int{http.StatusServiceUnavailable}, wantErr: true, requests: fetchAttempts},
		"refused":        {statuses: []int{http.StatusForbidden}, wantErr: true, requests: 1},
		"not found":      {statuses: []int{http.StatusNotFound}, wantErr: true, requests: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n := int(requests.Add(1))
				status := tt.statuses[min(n, len(tt.statuses))-1]
				if status != http.StatusOK {
					w.Header().Set("Retry-After", "0")
					http.Error(w, http.StatusText(status), status)
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer api.Close()
			h := &Store{client: api.Client(), base: api.URL + "/api/v2", token: "api-token", retryBase: time.Millisecond}
			_, err := h.fetch(t.Context(), api.URL+"/api/v2/x", "x", "application/json")
			if (err != nil) != tt.wantErr {
				t.Errorf("error %v, want one: %v", err, tt.wantErr)
			}
			if got := requests.Load(); got != tt.requests {
				t.Errorf("%d requests, want %d", got, tt.requests)
			}
		})
	}
}

// A cancelled context stops the waits between retries.
func TestFetchRetryStopsOnCancel(t *testing.T) {
	t.Parallel()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer api.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	h := &Store{client: api.Client(), base: api.URL + "/api/v2", token: "api-token"}
	start := time.Now()
	if _, err := h.fetch(ctx, api.URL+"/api/v2/x", "x", "application/json"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want the context's error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the wait went on for %s after the context ended", elapsed)
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for value, want := range map[string]time.Duration{
		"":                              0,
		"7":                             7 * time.Second,
		"-3":                            0,
		"soon":                          0,
		"Tue, 10 Nov 2026 12:00:05 GMT": 5 * time.Second,
		"Tue, 10 Nov 2026 11:00:00 GMT": 0,
	} {
		if got := parseRetryAfter(value, now); got != want {
			t.Errorf("%q: %s, want %s", value, got, want)
		}
	}
}

// Open hands over the state as it downloads, rather than once it is all
// there: the first of it reads while the host still holds back the rest,
// and the whole of it decodes to the resources it manages.
func TestOpenStreamsTheState(t *testing.T) {
	t.Parallel()
	const head = `{"version": 4, "resources": [{"mode": "managed", "type": "aws_s3_bucket", "name": "logs", "instances": [{"attributes": {"id": "logs"}}]}`
	release := make(chan struct{})
	archivist := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, head)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, `]}`)
	}))
	defer archivist.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		switch r.URL.Path {
		case "/api/v2/organizations/acme/workspaces/prod":
			body = map[string]any{"data": map[string]any{"id": "ws-prod"}}
		case "/api/v2/workspaces/ws-prod/current-state-version":
			body = map[string]any{"data": map[string]any{"attributes": map[string]any{"hosted-state-download-url": archivist.URL + "/signed"}}}
		case "/api/v2/organizations/acme/workspaces/empty":
			body = map[string]any{"data": map[string]any{"id": "ws-empty"}}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer api.Close()
	h := &Store{client: api.Client(), base: api.URL + "/api/v2", token: "api-token", retryBase: time.Millisecond}

	if in, err := h.Open(t.Context(), "acme", "empty"); err != nil || in != nil {
		t.Errorf("Open of a workspace without state: %v, %v", in, err)
	}
	in, err := h.Open(t.Context(), "acme", "prod")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	first := make([]byte, 16)
	if _, err := io.ReadFull(in, first); err != nil || string(first) != head[:16] {
		t.Fatalf("the start of the state, before the rest is sent: %q, %v", first, err)
	}
	close(release)
	found := managed.Resources{}
	if err := found.ParseFrom(io.MultiReader(strings.NewReader(string(first)), in), "acme/prod"); err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Errorf("the state manages %d resources, want 1: %v", len(found), found)
	}
}

// A throttled Open is asked again before it hands anything over, and its
// errors hide the signed URL as Get's do.
func TestOpenRetriesAndHidesTheURL(t *testing.T) {
	t.Parallel()
	var asks atomic.Int32
	archivist := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if asks.Add(1) == 1 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"version": 4}`)
	}))
	defer archivist.Close()
	h := &Store{client: archivist.Client(), base: archivist.URL + "/api/v2", token: "api-token", retryBase: time.Millisecond}
	in, err := h.open(t.Context(), archivist.URL+"/signed-c2VjcmV0", "downloading", "application/json")
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(in)
	in.Close()
	if string(content) != `{"version": 4}` || asks.Load() != 2 {
		t.Errorf("got %q after %d asks, want the state after 2", content, asks.Load())
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	_, err = h.open(t.Context(), closed.URL+"/signed-c2VjcmV0", "downloading", "application/json")
	if err == nil || strings.Contains(err.Error(), "signed-") {
		t.Errorf("want an error without the signed URL, got %v", err)
	}
}
