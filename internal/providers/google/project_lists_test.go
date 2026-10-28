// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/IgnatG/infraharvest/internal/ratelimit"
)

// A project's list that fails is kept, unless its context ended.
func TestProjectListsKeepFailures(t *testing.T) {
	t.Parallel()
	var lists projectLists[int]
	calls := 0
	denied := errors.New("denied")
	for range 2 {
		if _, err := lists.once("acme-prod", func() (int, error) { calls++; return 0, denied }); !errors.Is(err, denied) {
			t.Errorf("error %v, want %v", err, denied)
		}
	}
	if calls != 1 {
		t.Errorf("a failure: listed %d times, want once", calls)
	}
	for range 2 {
		if _, err := lists.once("acme-dev", func() (int, error) { calls++; return 0, context.Canceled }); !errors.Is(err, context.Canceled) {
			t.Errorf("error %v, want %v", err, context.Canceled)
		}
	}
	if calls != 3 {
		t.Errorf("a cancelled list: listed %d times, want twice", calls-1)
	}
}

// testRuns are the contexts of the tests' runs, by test (see testRun).
var testRuns sync.Map

// testRun returns the context of t's own run, made the first time t asks:
// the listings of a test share its caches, as the imports of a run do,
// and no other test sees them.
func testRun(t *testing.T) context.Context {
	if ctx, ok := testRuns.Load(t); ok {
		return ctx.(context.Context)
	}
	ctx, loaded := testRuns.LoadOrStore(t, ratelimit.NewContext(t.Context(), ratelimit.Like(ratelimit.Default())))
	if !loaded {
		t.Cleanup(func() { testRuns.Delete(t) })
	}
	return ctx.(context.Context)
}

// testCaches are the caches of t's run (see testRun).
func testCaches(t *testing.T) *runCaches {
	return cachesOf(testRun(t))
}

// Calls outside any run share nothing: what one lists, another lists
// afresh.
func TestCachesOfNoRunAreNobodys(t *testing.T) {
	t.Parallel()
	first, second := cachesOf(t.Context()), cachesOf(context.Background())
	if first == second {
		t.Error("two calls outside any run share their caches")
	}
	if testCaches(t) != cachesOf(testRun(t)) {
		t.Error("the calls of a run don't share its caches")
	}
}
