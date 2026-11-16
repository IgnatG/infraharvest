// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package runstate is the state one run of a command keeps for all its
// listings: its rate limiter, and what is built once for the whole run,
// such as the SDK configurations, the credentials the providers resolved
// and the clients they limit (see Kept). A run puts its Run in the context
// its listings get (see NewContext), so that runs in one process, at once
// or one after another, never share what they keep or reset each other's
// rate.
package runstate

import (
	"context"
	"sync"

	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

// Run is the state of one run: its limiter and the values kept for it.
type Run struct {
	limiter *ratelimit.Limiter

	// mu guards kept.
	mu sync.Mutex
	// kept are the values kept with the run, by key (see Kept).
	kept map[any]any
}

// New returns the state of a run that limits its requests with l.
func New(l *ratelimit.Limiter) *Run {
	return &Run{limiter: l}
}

var std = New(ratelimit.Default())

// Default is the run of the calls whose context carries none (see
// FromContext): it limits with ratelimit.Default, and what it keeps lasts
// for the process. It is a fixed fallback: a command's run has a Run of
// its own (see NewContext).
func Default() *Run {
	return std
}

// Limiter returns the run's limiter; for a nil Run, ratelimit.Default.
func (r *Run) Limiter() *ratelimit.Limiter {
	if r == nil {
		return ratelimit.Default()
	}
	return r.limiter
}

// contextKey is the key of the Run NewContext puts in a context.
type contextKey struct{}

// NewContext returns ctx carrying r, and r's limiter (see
// ratelimit.FromContext).
func NewContext(ctx context.Context, r *Run) context.Context {
	return context.WithValue(ratelimit.NewContext(ctx, r.Limiter()), contextKey{}, r)
}

// FromContext returns the Run NewContext put in ctx, or Default if there
// is none.
func FromContext(ctx context.Context) *Run {
	if r, ok := ctx.Value(contextKey{}).(*Run); ok && r != nil {
		return r
	}
	return std
}

// Kept returns the value kept with r for key, which build makes the first
// time it is asked for: it lasts as long as the run, and no other run sees
// it. build runs with r's values locked, so it must be quick and must not
// ask r for another. A nil Run keeps nothing: each call makes the value
// again.
func Kept[T any](r *Run, key any, build func() T) T {
	if r == nil {
		return build()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if value, ok := r.kept[key]; ok {
		return value.(T)
	}
	value := build()
	if r.kept == nil {
		r.kept = map[any]any{}
	}
	r.kept[key] = value
	return value
}
