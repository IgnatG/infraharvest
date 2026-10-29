// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package providertest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// A cloud answers each request with its handler, as a client and as a
// server, and records the requests in order; one whose context is done
// gets the context's error and isn't recorded.
func TestCloud(t *testing.T) {
	t.Parallel()
	cloud := New(func(r Request) Answer {
		if r.Path == "/missing" {
			return Status(http.StatusNotFound, `{"error":"`+r.Body+`"}`)
		}
		return Answer{ContentType: "text/xml", Header: http.Header{"X-Page": []string{r.Query.Get("page")}}, Body: "<ok/>"}
	})
	for _, tc := range []struct {
		name                 string
		send                 func(*http.Request) (*http.Response, error)
		url                  string
		status               int
		contentType, page    string
		body, wantAnswerBody string
	}{
		{"client", cloud.Do, "https://cloud.test/list?page=2", http.StatusOK, "text/xml", "2", "", "<ok/>"},
		{"round tripper", cloud.RoundTrip, "https://cloud.test/missing", http.StatusNotFound, "application/json", "", "why", `{"error":"why"}`},
		{"server", http.DefaultClient.Do, cloud.Serve(t) + "/list?page=3", http.StatusOK, "text/xml", "3", "", "<ok/>"},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, tc.url, strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := tc.send(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.status || resp.Header.Get("Content-Type") != tc.contentType || resp.Header.Get("X-Page") != tc.page || string(body) != tc.wantAnswerBody {
			t.Errorf("%s: got %d %q %q %q", tc.name, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("X-Page"), body)
		}
	}
	if got, want := cloud.Paths(), []string{"/list", "/missing", "/list"}; !slices.Equal(got, want) {
		t.Errorf("paths %v, want %v", got, want)
	}
	if got, want := cloud.Bodies(), []string{"", "why", ""}; !slices.Equal(got, want) {
		t.Errorf("bodies %q, want %q", got, want)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloud.test/list", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloud.Do(req); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled request: %v, want context.Canceled", err)
	}
	if n := len(cloud.Requests()); n != 3 {
		t.Errorf("%d requests recorded, want the canceled one left out", n)
	}
}

// Pages answers with each body in turn, then with a 500.
func TestPages(t *testing.T) {
	t.Parallel()
	pages := Pages("one", "two")
	var got []string
	for range 3 {
		a := pages(Request{})
		got = append(got, a.Body)
		if a.status() == http.StatusInternalServerError {
			got[len(got)-1] = "500"
		}
	}
	if want := []string{"one", "two", "500"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
