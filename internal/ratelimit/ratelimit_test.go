// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"
)

// Past the burst, requests to a host wait their turn; other hosts don't.
// In a synctest bubble the clock is virtual, so the waits are exact and
// take no real time, however loaded the runner.
func TestLimiterSpacesRequestsToAHost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := New(20)
		start := time.Now()
		for range 25 {
			if err := l.Wait(t.Context(), "ec2.eu-west-1.amazonaws.com"); err != nil {
				t.Fatal(err)
			}
		}
		// 20 at once, then 5 at 20 a second.
		if elapsed := time.Since(start); elapsed < 200*time.Millisecond || elapsed > 300*time.Millisecond {
			t.Errorf("25 requests took %s, want 200ms to 300ms", elapsed)
		}
		start = time.Now()
		if err := l.Wait(t.Context(), "s3.eu-west-1.amazonaws.com"); err != nil || time.Since(start) != 0 {
			t.Errorf("another host waited %s (%v)", time.Since(start), err)
		}
	})
}

func TestLimiterStopsWithTheContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := New(1)
		if err := l.Wait(t.Context(), "h"); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		defer cancel()
		if err := l.Wait(ctx, "h"); err == nil {
			t.Error("want an error once the context is done")
		}
	})
}

func TestNoRateLimitsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var none *Limiter
		for _, l := range []*Limiter{none, New(0)} {
			start := time.Now()
			for range 100 {
				if err := l.Wait(t.Context(), "h"); err != nil {
					t.Fatal(err)
				}
			}
			if time.Since(start) != 0 {
				t.Errorf("%v waited %s", l, time.Since(start))
			}
		}
	})
}

func TestTransportAndClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	for name, through := range map[string]func(*Limiter) func(*http.Request) (*http.Response, error){
		"transport": func(l *Limiter) func(*http.Request) (*http.Response, error) {
			return (&http.Client{Transport: l.Transport(nil)}).Do
		},
		"client": func(l *Limiter) func(*http.Request) (*http.Response, error) { return l.Client(http.DefaultClient).Do },
	} {
		do := through(New(1))
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
		resp, err := do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		resp.Body.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		req, _ = http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		if _, err := do(req); err == nil {
			t.Errorf("%s: the second request went at once", name)
		}
		cancel()
	}
}
