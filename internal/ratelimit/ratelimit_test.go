// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package ratelimit

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

// Default is one Limiter, at DefaultRate until SetDefault changes it; Like
// copies a Limiter's rate into a Limiter of its own.
func TestDefaultAndLike(t *testing.T) {
	t.Cleanup(func() { SetDefault(DefaultRate) })
	if first := Default(); Default() != first {
		t.Fatal("Default returns another Limiter each time")
	}
	if got := Default().Rate(); got != DefaultRate {
		t.Errorf("default rate %v, want %v", got, DefaultRate)
	}
	SetDefault(5)
	if got := Default().Rate(); got != 5 {
		t.Errorf("rate after SetDefault(5) %v", got)
	}
	like := Like(Default())
	if like == Default() || like.Rate() != 5 {
		t.Errorf("Like: %p (default %p), rate %v; want a Limiter of its own at 5", like, Default(), like.Rate())
	}
	// It doesn't follow the one it was made like.
	SetDefault(7)
	if like.Rate() != 5 {
		t.Errorf("Like's rate followed the default's: %v", like.Rate())
	}
}

// A new rate applies to every host from the next request, with a burst of
// its own; setting the rate it has keeps the hosts' waits.
func TestSetRate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const host = "compute.googleapis.com"
		l := New(1)
		if err := l.Wait(t.Context(), host); err != nil {
			t.Fatal(err)
		}
		// The same rate: the host has used its burst, so waits a second.
		l.SetRate(1)
		start := time.Now()
		if err := l.Wait(t.Context(), host); err != nil || time.Since(start) != time.Second {
			t.Errorf("same rate: waited %s (%v), want 1s", time.Since(start), err)
		}
		// Another rate: a fresh burst of 10, then a tenth of a second each.
		l.SetRate(10)
		start = time.Now()
		for range 11 {
			if err := l.Wait(t.Context(), host); err != nil {
				t.Fatal(err)
			}
		}
		if elapsed := time.Since(start); elapsed != 100*time.Millisecond {
			t.Errorf("11 requests at 10 a second took %s, want 100ms", elapsed)
		}
		// No rate: nothing waits.
		l.SetRate(0)
		start = time.Now()
		for range 50 {
			if err := l.Wait(t.Context(), host); err != nil {
				t.Fatal(err)
			}
		}
		if time.Since(start) != 0 || l.Rate() != 0 {
			t.Errorf("no rate: waited %s, rate %v", time.Since(start), l.Rate())
		}
	})
}

// A rate below one a second still lets one request go at once, then
// spaces them by its inverse.
func TestFractionalRate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := New(0.5)
		start := time.Now()
		for range 2 {
			if err := l.Wait(t.Context(), "h"); err != nil {
				t.Fatal(err)
			}
		}
		if elapsed := time.Since(start); elapsed != 2*time.Second {
			t.Errorf("2 requests at 0.5 a second took %s, want 2s", elapsed)
		}
	})
}

// Requests from many goroutines, to several hosts, while the rate
// changes, share each host's limit without racing: run with -race.
func TestConcurrentWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := New(10)
		hosts := []string{"a", "b", "c"}
		var wg sync.WaitGroup
		var sent atomic.Int64
		for i := range 30 {
			wg.Go(func() {
				if err := l.Wait(t.Context(), hosts[i%len(hosts)]); err != nil {
					t.Error(err)
					return
				}
				sent.Add(1)
			})
		}
		wg.Go(func() {
			l.SetRate(10)
			_ = l.Rate()
		})
		wg.Wait()
		if sent.Load() != 30 {
			t.Errorf("%d requests went, want 30", sent.Load())
		}
	})
}

// recordingTransport records whether a request reached it.
type recordingTransport struct{ called bool }

func (r *recordingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.called = true
	return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
}

func (r *recordingTransport) Do(req *http.Request) (*http.Response, error) { return r.RoundTrip(req) }

// closeRecorder records whether it was closed.
type closeRecorder struct {
	io.Reader
	closed bool
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return nil
}

// A request its host's rate holds back until the context is done isn't
// sent: the transport closes its body, as a RoundTripper must, and
// returns the context's error, as the client does; one it lets go reaches
// the base given.
func TestTransportAndClientHoldBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, tc := range []struct {
			name string
			send func(l *Limiter, base *recordingTransport, req *http.Request) (*http.Response, error)
		}{
			{"transport", func(l *Limiter, base *recordingTransport, req *http.Request) (*http.Response, error) {
				return l.Transport(base).RoundTrip(req)
			}},
			{"client", func(l *Limiter, base *recordingTransport, req *http.Request) (*http.Response, error) {
				return l.Client(base).Do(req)
			}},
		} {
			l := New(1)
			base := &recordingTransport{}
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://sqs.eu-west-2.amazonaws.com/", nil)
			resp, err := tc.send(l, base, req)
			if err != nil || !base.called {
				t.Fatalf("%s: the first request: %v, sent %t", tc.name, err, base.called)
			}
			resp.Body.Close()

			base.called = false
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
			body := &closeRecorder{Reader: strings.NewReader("Action=ListQueues")}
			req, _ = http.NewRequestWithContext(ctx, http.MethodPost, "https://sqs.eu-west-2.amazonaws.com/", body)
			_, err = tc.send(l, base, req)
			cancel()
			if err == nil || base.called {
				t.Errorf("%s: the held-back request: %v, sent %t", tc.name, err, base.called)
			}
			if tc.name == "transport" && !body.closed {
				t.Error("transport: the held-back request's body wasn't closed")
			}
		}
	})
}

// With no base, the transport and the client send through the standard
// library's defaults.
func TestTransportAndClientDefaultBase(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	l := New(0)
	for name, do := range map[string]func(*http.Request) (*http.Response, error){
		"transport": l.Transport(nil).RoundTrip,
		"client":    l.Client(nil).Do,
	} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
		resp, err := do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("%s: status %d", name, resp.StatusCode)
		}
	}
}
