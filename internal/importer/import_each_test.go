// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/ratelimit"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// recordScope does what importInto does with a scope's run, in short.
func recordScope(run *Run, o Options, scope string) {
	run.used = true
	run.options = o
	run.discovered["aws_vpc"]++
	run.report.AddDiscovered("aws/"+scope+"/eu-west-2", 1)
	run.report.Directories = append(run.report.Directories, report.Directory{Path: "aws/" + scope + "/eu-west-2", Scope: "aws/" + scope + "/eu-west-2"})
}

func TestImportEachInParallel(t *testing.T) {
	run := NewRun()
	run.selection = &selection.File{Version: selection.Version, Defaults: selection.Defaults{Include: true}}
	options := Options{Out: t.TempDir(), Parallel: 3}
	scopes := []string{"111111111111", "222222222222", "333333333333", "444444444444", "555555555555"}

	var running, most atomic.Int32
	// The scopes after the first wait for each other, so the test fails
	// rather than passes if they run one at a time.
	var started sync.WaitGroup
	started.Add(3)
	importOne := func(scopeRun *Run, o Options, scope string) error {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		if scopeRun == nil || scopeRun == run {
			t.Errorf("%s: imported into the command's run", scope)
		}
		if scopeRun.selection != run.selection {
			t.Errorf("%s: doesn't share the selection file", scope)
		}
		switch scope {
		case scopes[0]:
			if n != 1 {
				t.Errorf("the first scope runs alone, with %d", n)
			}
			scopeRun.lock = []byte("lock")
		case scopes[1], scopes[2], scopes[3]:
			if string(scopeRun.lock) != "lock" {
				t.Errorf("%s: lock %q, want the first scope's", scope, scopeRun.lock)
			}
			started.Done()
			waitOrFail(t, &started)
		}
		if scope == scopes[2] {
			return errors.New("AccessDenied: can't assume the role")
		}
		recordScope(scopeRun, o, scope)
		return nil
	}

	if err := run.ImportEach(options, "account", scopes, importOne); err != nil {
		t.Fatalf("the run must go on: %v", err)
	}
	if most.Load() != 3 {
		t.Errorf("at most %d at once, want 3", most.Load())
	}
	if run.discovered["aws_vpc"] != 4 {
		t.Errorf("discovered %v", run.discovered)
	}
	// In the order of scopes, whichever finished first.
	var paths []string
	for _, d := range run.report.Directories {
		paths = append(paths, strings.Split(d.Path, "/")[1])
	}
	if want := []string{scopes[0], scopes[1], scopes[3], scopes[4]}; strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Errorf("directories %v, want %v", paths, want)
	}
	if len(run.report.Scopes) != 4 {
		t.Errorf("scopes %+v", run.report.Scopes)
	}
	if len(run.failures) != 1 || !strings.Contains(run.failures[0].Error(), "account 333333333333: AccessDenied") {
		t.Errorf("failures %v", run.failures)
	}
	if string(run.lock) != "lock" || !run.used {
		t.Errorf("lock %q, used %v", run.lock, run.used)
	}
}

// An interrupt in the first scope starts no others; one later keeps what
// finished.
func TestImportEachInterrupted(t *testing.T) {
	run := NewRun()
	options := Options{Out: t.TempDir(), Parallel: 2}

	var calls atomic.Int32
	err := run.ImportEach(options, "project", []string{"a", "b", "c"}, func(*Run, Options, string) error {
		calls.Add(1)
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Errorf("interrupted first: %v after %d calls", err, calls.Load())
	}

	run = NewRun()
	err = run.ImportEach(options, "project", []string{"a", "b"}, func(scopeRun *Run, o Options, scope string) error {
		if scope == "b" {
			return context.Canceled
		}
		recordScope(scopeRun, o, scope)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("interrupted later: %v", err)
	}
	if len(run.report.Directories) != 1 || len(run.failures) != 0 {
		t.Errorf("directories %v, failures %v", run.report.Directories, run.failures)
	}
}

// waitOrFail waits for wg, failing the test instead of hanging.
func waitOrFail(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("the scopes didn't run at the same time")
	}
}

// A scope that panics fails, and the others go on, one at a time or in
// parallel.
func TestImportEachRecoversAPanic(t *testing.T) {
	for _, parallel := range []int{1, 2} {
		run := NewRun()
		options := Options{Out: t.TempDir(), Parallel: parallel}
		err := run.ImportEach(options, "account", []string{"a", "b", "c"}, func(scopeRun *Run, o Options, scope string) error {
			if scope == "b" {
				panic("unexpected response")
			}
			recordScope(scopeRun, o, scope)
			return nil
		})
		if err != nil {
			t.Errorf("parallel %d: %v", parallel, err)
		}
		if len(run.failures) != 1 || !strings.Contains(run.failures[0].Error(), "account b: panic: unexpected response") {
			t.Errorf("parallel %d: failures %v", parallel, run.failures)
		}
		if run.discovered["aws_vpc"] != 2 {
			t.Errorf("parallel %d: discovered %v", parallel, run.discovered)
		}
	}
}

// The calls a command makes before its Import calls list with the limiter
// its Import calls get, in a child's run too, so that they share what the
// providers keep for the run.
func TestWithLimiterIsTheImportCallsLimiter(t *testing.T) {
	t.Parallel()
	r := NewRun()
	limiter := ratelimit.FromContext(r.WithLimiter(t.Context(), 5))
	if limiter == ratelimit.Default() || limiter.Rate() != 5 {
		t.Fatalf("got a limiter of %v a second, Default's: %t", limiter.Rate(), limiter == ratelimit.Default())
	}
	if r.shared.rateLimiter(5) != limiter || r.child().shared.rateLimiter(5) != limiter {
		t.Error("the Import calls get another limiter")
	}
}
