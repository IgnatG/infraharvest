// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package runstate

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

// Kept makes a key's value once for each Run, however many ask at once,
// and another Run has its own; a nil Run keeps nothing. Run with -race.
func TestKept(t *testing.T) {
	type key struct{}
	var made atomic.Int64
	value := func() *int {
		made.Add(1)
		return new(int)
	}
	run, other := New(ratelimit.New(1)), New(ratelimit.New(1))
	got := make([]*int, 20)
	var wg sync.WaitGroup
	for i := range got {
		wg.Go(func() { got[i] = Kept(run, key{}, value) })
	}
	wg.Wait()
	for _, v := range got {
		if v != got[0] {
			t.Fatal("one Run kept two values of a key")
		}
	}
	if made.Load() != 1 {
		t.Errorf("made %d values, want 1", made.Load())
	}
	if Kept(other, key{}, value) == got[0] {
		t.Error("two Runs share a value")
	}
	if Kept(run, "another key", value) == got[0] {
		t.Error("two keys share a value")
	}
	var none *Run
	if first := Kept(none, key{}, value); Kept(none, key{}, value) == first {
		t.Error("a nil Run kept a value")
	}
}

// NewContext puts the Run and its limiter in the context; without one,
// FromContext is Default, which limits with ratelimit.Default.
func TestContext(t *testing.T) {
	l := ratelimit.New(3)
	r := New(l)
	ctx := NewContext(t.Context(), r)
	if got := FromContext(ctx); got != r {
		t.Errorf("FromContext = %p, want %p", got, r)
	}
	if got := ratelimit.FromContext(ctx); got != l {
		t.Errorf("ratelimit.FromContext = %p, want the run's %p", got, l)
	}
	if got := FromContext(t.Context()); got != Default() {
		t.Errorf("FromContext without one = %p, want Default %p", got, Default())
	}
	if Default().Limiter() != ratelimit.Default() {
		t.Error("Default doesn't limit with ratelimit.Default")
	}
}
