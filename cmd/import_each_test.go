// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/report"
	"github.com/IgnatG/infraharvest/selection"
)

// recordScope does what importInto does with a scope's run, in short.
func recordScope(o ImportOptions, scope string) {
	o.run.used = true
	o.run.options = o
	o.run.discovered["aws_vpc"]++
	o.run.report.AddDiscovered("aws/"+scope+"/eu-west-2", 1)
	o.run.report.Directories = append(o.run.report.Directories, report.Directory{Path: "aws/" + scope + "/eu-west-2", Scope: "aws/" + scope + "/eu-west-2"})
}

func TestImportEachInParallel(t *testing.T) {
	activeRun = newEngineRun()
	defer func() { activeRun = nil }()
	activeRun.selection = &selection.File{Version: selection.Version, Defaults: selection.Defaults{Include: true}}
	options := ImportOptions{PathOutput: t.TempDir(), Parallel: 3}
	scopes := []string{"111111111111", "222222222222", "333333333333", "444444444444", "555555555555"}

	var running, most atomic.Int32
	// The scopes after the first wait for each other, so the test fails
	// rather than passes if they run one at a time.
	var started sync.WaitGroup
	started.Add(3)
	importOne := func(o ImportOptions, scope string) error {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		if o.run == nil || o.run == activeRun {
			t.Errorf("%s: imported into the command's run", scope)
		}
		if o.run.selection != activeRun.selection {
			t.Errorf("%s: doesn't share the selection file", scope)
		}
		switch scope {
		case scopes[0]:
			if n != 1 {
				t.Errorf("the first scope runs alone, with %d", n)
			}
			o.run.lock = []byte("lock")
		case scopes[1], scopes[2], scopes[3]:
			if string(o.run.lock) != "lock" {
				t.Errorf("%s: lock %q, want the first scope's", scope, o.run.lock)
			}
			started.Done()
			waitOrFail(t, &started)
		}
		if scope == scopes[2] {
			return errors.New("AccessDenied: can't assume the role")
		}
		recordScope(o, scope)
		return nil
	}

	if err := importEach(options, "account", scopes, importOne); err != nil {
		t.Fatalf("the run must go on: %v", err)
	}
	if most.Load() != 3 {
		t.Errorf("at most %d at once, want 3", most.Load())
	}
	if activeRun.discovered["aws_vpc"] != 4 {
		t.Errorf("discovered %v", activeRun.discovered)
	}
	// In the order of scopes, whichever finished first.
	var paths []string
	for _, d := range activeRun.report.Directories {
		paths = append(paths, strings.Split(d.Path, "/")[1])
	}
	if want := []string{scopes[0], scopes[1], scopes[3], scopes[4]}; strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Errorf("directories %v, want %v", paths, want)
	}
	if len(activeRun.report.Scopes) != 4 {
		t.Errorf("scopes %+v", activeRun.report.Scopes)
	}
	if len(activeRun.failures) != 1 || !strings.Contains(activeRun.failures[0].Error(), "account 333333333333: AccessDenied") {
		t.Errorf("failures %v", activeRun.failures)
	}
	if string(activeRun.lock) != "lock" || !activeRun.used {
		t.Errorf("lock %q, used %v", activeRun.lock, activeRun.used)
	}
}

// An interrupt in the first scope starts no others; one later keeps what
// finished.
func TestImportEachInterrupted(t *testing.T) {
	activeRun = newEngineRun()
	defer func() { activeRun = nil }()
	options := ImportOptions{PathOutput: t.TempDir(), Parallel: 2}

	var calls atomic.Int32
	err := importEach(options, "project", []string{"a", "b", "c"}, func(ImportOptions, string) error {
		calls.Add(1)
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Errorf("interrupted first: %v after %d calls", err, calls.Load())
	}

	activeRun = newEngineRun()
	err = importEach(options, "project", []string{"a", "b"}, func(o ImportOptions, scope string) error {
		if scope == "b" {
			return context.Canceled
		}
		recordScope(o, scope)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("interrupted later: %v", err)
	}
	if len(activeRun.report.Directories) != 1 || len(activeRun.failures) != 0 {
		t.Errorf("directories %v, failures %v", activeRun.report.Directories, activeRun.failures)
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
