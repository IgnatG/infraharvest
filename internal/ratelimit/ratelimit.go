// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package ratelimit limits the requests a run sends to each API host, such
// as ec2.eu-west-1.amazonaws.com or compute.googleapis.com: the clouds
// throttle by API, and a run lists many services, regions and accounts at
// once. The clients of the listers send their requests through their
// run's Limiter (see FromContext).
package ratelimit

import (
	"context"
	"net/http"
	"sync"

	"golang.org/x/time/rate"
)

// DefaultRate is how many requests a second each API host gets, unless
// --rate-limit says otherwise: below the read limits the clouds document,
// such as EC2's 20 a second refill for describe calls.
const DefaultRate = 20

// Limiter limits the requests to each host to a rate, with bursts of up
// to that many requests. A nil Limiter, or one with no rate, limits
// nothing. Its rate is fixed: a run with another rate has a Limiter of
// its own.
type Limiter struct {
	rate float64

	// mu guards hosts.
	mu    sync.Mutex
	hosts map[string]*rate.Limiter

	// keptMu guards kept, apart from mu, so that making a value never
	// holds up a request.
	keptMu sync.Mutex
	// kept are the values kept with the Limiter, by key (see Kept).
	kept map[any]any
}

// New returns a Limiter of perSecond requests a second to each host; 0 or
// below limits nothing.
func New(perSecond float64) *Limiter {
	return &Limiter{rate: perSecond, hosts: map[string]*rate.Limiter{}}
}

var std = New(DefaultRate)

// Default is the Limiter, at DefaultRate, of the calls whose context
// carries no run's (see FromContext), such as those a command makes before
// its runs, to find the accounts it imports. It is a fixed fallback: a run
// limits its own calls with a Limiter of its own (see NewContext).
func Default() *Limiter {
	return std
}

// Like returns a new Limiter with l's rate.
func Like(l *Limiter) *Limiter {
	return New(l.Rate())
}

// contextKey is the key of the Limiter NewContext puts in a context.
type contextKey struct{}

// NewContext returns ctx carrying l, the Limiter of the run whose calls
// list with ctx: each run has its own, so that runs in one process, at
// once or one after another, never reset each other's rate.
func NewContext(ctx context.Context, l *Limiter) context.Context {
	return context.WithValue(ctx, contextKey{}, l)
}

// FromContext returns the Limiter NewContext put in ctx, or Default if
// there is none.
func FromContext(ctx context.Context) *Limiter {
	if l, ok := ctx.Value(contextKey{}).(*Limiter); ok {
		return l
	}
	return std
}

// Kept returns the value kept with l for key, which build makes the first
// time it is asked for. A run's Limiter is the one thing every listing of
// the run shares (see NewContext), so the providers keep with it what
// the run builds once for all of them, such as the clients it limits: it
// lasts as long as the run, and no other run sees it. build runs with l's
// values locked, so it must be quick and must not ask l for another. A
// nil Limiter keeps nothing: each call makes the value again.
func Kept[T any](l *Limiter, key any, build func() T) T {
	if l == nil {
		return build()
	}
	l.keptMu.Lock()
	defer l.keptMu.Unlock()
	if value, ok := l.kept[key]; ok {
		return value.(T)
	}
	value := build()
	if l.kept == nil {
		l.kept = map[any]any{}
	}
	l.kept[key] = value
	return value
}

// Rate is the requests a second each host gets; 0 or below: no limit.
func (l *Limiter) Rate() float64 {
	return l.rate
}

// Wait blocks until a request to host may go, or ctx is done.
func (l *Limiter) Wait(ctx context.Context, host string) error {
	if l == nil || l.rate <= 0 {
		return nil
	}
	l.mu.Lock()
	limiter, ok := l.hosts[host]
	if !ok {
		limiter = rate.NewLimiter(rate.Limit(l.rate), max(int(l.rate), 1))
		l.hosts[host] = limiter
	}
	l.mu.Unlock()
	return limiter.Wait(ctx)
}

// Transport sends each request through base once its host's rate allows.
func (l *Limiter) Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &transport{limiter: l, base: base}
}

type transport struct {
	limiter *Limiter
	base    http.RoundTripper
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.limiter.Wait(req.Context(), req.URL.Host); err != nil {
		// A RoundTripper closes the body, even when it sends nothing.
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	return t.base.RoundTrip(req)
}

// Doer is an HTTP client, such as the AWS SDK's or azcore's transporter.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client sends each request through base, or http.DefaultClient, once its
// host's rate allows.
func (l *Limiter) Client(base Doer) Doer {
	if base == nil {
		base = http.DefaultClient
	}
	return &client{limiter: l, base: base}
}

type client struct {
	limiter *Limiter
	base    Doer
}

func (c *client) Do(req *http.Request) (*http.Response, error) {
	if err := c.limiter.Wait(req.Context(), req.URL.Host); err != nil {
		return nil, err
	}
	return c.base.Do(req)
}
