// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package ratelimit limits the requests a run sends to each API host, such
// as ec2.eu-west-1.amazonaws.com or compute.googleapis.com: the clouds
// throttle by API, and a run lists many services, regions and accounts at
// once. The clients of the listers send their requests through Default.
package ratelimit

import (
	"context"
	"net/http"
	"sync"

	"golang.org/x/time/rate"
)

// DefaultRate is how many requests a second each API host gets, unless
// SetDefault says otherwise: below the read limits the clouds document,
// such as EC2's 20 a second refill for describe calls.
const DefaultRate = 20

// Limiter limits the requests to each host to a rate, with bursts of up
// to that many requests. A nil Limiter, or one with no rate, limits
// nothing.
type Limiter struct {
	mu    sync.Mutex
	rate  float64
	hosts map[string]*rate.Limiter
}

// New returns a Limiter of perSecond requests a second to each host; 0 or
// below limits nothing.
func New(perSecond float64) *Limiter {
	return &Limiter{rate: perSecond, hosts: map[string]*rate.Limiter{}}
}

var std = New(DefaultRate)

// Default is the Limiter of the listers' clients, or the rate of those
// that limit each account on its own (see Like).
func Default() *Limiter {
	return std
}

// Like returns a new Limiter with l's rate.
func Like(l *Limiter) *Limiter {
	return New(l.Rate())
}

// SetDefault sets Default's rate, such as from --rate-limit.
func SetDefault(perSecond float64) {
	std.SetRate(perSecond)
}

// Rate is the requests a second each host gets; 0 or below: no limit.
func (l *Limiter) Rate() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rate
}

// SetRate sets the rate of every host, for the requests from then on.
func (l *Limiter) SetRate(perSecond float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if perSecond == l.rate {
		return
	}
	l.rate = perSecond
	l.hosts = map[string]*rate.Limiter{}
}

// Wait blocks until a request to host may go, or ctx is done.
func (l *Limiter) Wait(ctx context.Context, host string) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if l.rate <= 0 {
		l.mu.Unlock()
		return nil
	}
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
