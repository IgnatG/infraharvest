// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// fakeSteps list and generate calls as listCall and generate do, in
// short: each call, named by its first argument, lists one resource into a
// root of its own. They record what happened, in order.
type fakeSteps struct {
	mu     sync.Mutex
	events []string
	roots  map[string]map[string][]engine.Import
	locks  map[string]string
	// list, if set, runs as each call lists, and its error fails it.
	list func(ctx context.Context, call string) error
}

func newFakeSteps() *fakeSteps {
	return &fakeSteps{roots: map[string]map[string][]engine.Import{}, locks: map[string]string{}}
}

func (f *fakeSteps) record(event string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}

func (f *fakeSteps) steps() callSteps {
	return callSteps{
		list: func(ctx context.Context, run *Run, c Call) (*listedCall, error) {
			name := c.Args[0]
			f.record("list " + name)
			if f.list != nil {
				if err := f.list(ctx, name); err != nil {
					return nil, err
				}
			}
			run.used = true
			run.options = c.Options
			run.discovered["aws_vpc"]++
			run.report.AddDiscovered("aws/111/"+name, 1)
			if c.Options.Discover {
				return nil, nil
			}
			byDir := map[string][]engine.Import{filepath.Join("out", name): {{Type: "aws_vpc", ID: name, Name: name}}}
			return &listedCall{run: run, options: c.Options, scope: "aws/111/" + name, byDir: byDir}, nil
		},
		generate: func(_ context.Context, c *listedCall, roots map[string][]engine.Import) error {
			name := strings.TrimPrefix(c.scope, "aws/111/")
			f.record("generate " + name)
			f.mu.Lock()
			f.roots[name] = roots
			f.locks[name] = string(c.run.lock)
			f.mu.Unlock()
			if c.run.lock == nil {
				c.run.lock = []byte("lock of " + name)
			}
			c.run.report.Directories = append(c.run.report.Directories, report.Directory{Path: name, Scope: c.scope})
			return nil
		},
	}
}

// fakeCalls returns a call for each name, as its first argument.
func fakeCalls(options Options, names ...string) []Call {
	calls := make([]Call, len(names))
	for i, name := range names {
		calls[i] = Call{Options: options, Args: []string{name}}
	}
	return calls
}

// Every call of a scope lists before any generates, and each root then
// sees the roots of every other call, before and after it; the calls
// record in their order, and the first lock file seeds the others.
func TestImportCallsListAllThenGenerate(t *testing.T) {
	run := NewRun()
	f := newFakeSteps()
	if err := importCalls(t.Context(), run, fakeCalls(Options{Out: "out", All: true}, "global", "us-east-1", "eu-west-1"), f.steps()); err != nil {
		t.Fatal(err)
	}
	want := []string{"list global", "list us-east-1", "list eu-west-1", "generate global", "generate us-east-1", "generate eu-west-1"}
	if !slices.Equal(f.events, want) {
		t.Errorf("events %v, want %v", f.events, want)
	}
	for _, name := range []string{"global", "eu-west-1"} {
		roots := f.roots[name]
		if len(roots) != 3 || roots[filepath.Join("out", "global")] == nil || roots[filepath.Join("out", "eu-west-1")] == nil {
			t.Errorf("%s: roots %v, want every call's", name, roots)
		}
	}
	if want := map[string]string{"global": "", "us-east-1": "lock of global", "eu-west-1": "lock of global"}; !maps.Equal(f.locks, want) {
		t.Errorf("locks %v, want %v", f.locks, want)
	}
	var paths []string
	for _, d := range run.report.Directories {
		paths = append(paths, d.Path)
	}
	if want := []string{"global", "us-east-1", "eu-west-1"}; !slices.Equal(paths, want) {
		t.Errorf("directories %v, want %v", paths, want)
	}
	if run.discovered["aws_vpc"] != 3 || len(run.report.Scopes) != 3 || !run.used || string(run.lock) != "lock of global" {
		t.Errorf("discovered %v, scopes %v, used %v, lock %q", run.discovered, run.report.Scopes, run.used, run.lock)
	}
}

// A call that fails to list fails the scope: the calls after it don't
// list, nothing is generated, and what the calls before it found is still
// in the run.
func TestImportCallsStopAtTheFirstFailure(t *testing.T) {
	run := NewRun()
	f := newFakeSteps()
	f.list = func(_ context.Context, call string) error {
		if call == "eu-west-1" {
			return errors.New("AccessDenied: the region is disabled")
		}
		return nil
	}
	err := importCalls(t.Context(), run, fakeCalls(Options{Out: "out", All: true}, "global", "eu-west-1", "eu-west-2"), f.steps())
	if err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("want the listing's error, got %v", err)
	}
	if want := []string{"list global", "list eu-west-1"}; !slices.Equal(f.events, want) {
		t.Errorf("events %v, want %v", f.events, want)
	}
	if run.discovered["aws_vpc"] != 1 {
		t.Errorf("discovered %v, want the first call's", run.discovered)
	}
}

// Discover lists every call and generates nothing.
func TestImportCallsDiscover(t *testing.T) {
	run := NewRun()
	f := newFakeSteps()
	if err := importCalls(t.Context(), run, fakeCalls(Options{Out: "out", Discover: true}, "global", "eu-west-1"), f.steps()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"list global", "list eu-west-1"}; !slices.Equal(f.events, want) {
		t.Errorf("events %v, want %v", f.events, want)
	}
	if run.discovered["aws_vpc"] != 2 {
		t.Errorf("discovered %v", run.discovered)
	}
}

// The First calls list and generate one at a time, before the others,
// which then run --parallel at once; the run still records in the order
// of calls, and the first lock file seeds the others.
func TestImportCallsInParallel(t *testing.T) {
	run := NewRun()
	f := newFakeSteps()
	var running, most atomic.Int32
	// The regions after the First calls wait for each other, so the test
	// fails rather than passes if they run one at a time.
	var started sync.WaitGroup
	started.Add(3)
	f.list = func(_ context.Context, call string) error {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		switch call {
		case "global", "us-east-1":
			if n != 1 {
				t.Errorf("%s runs alone, with %d", call, n)
			}
		case "eu-west-1", "eu-west-2", "eu-west-3":
			started.Done()
			waitOrFail(t, &started)
		}
		return nil
	}
	calls := fakeCalls(Options{Out: "out", All: true, Parallel: 3}, "global", "us-east-1", "eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1")
	calls[0].First, calls[1].First = true, true
	if err := importCalls(t.Context(), run, calls, f.steps()); err != nil {
		t.Fatal(err)
	}
	if most.Load() != 3 {
		t.Errorf("at most %d at once, want 3", most.Load())
	}
	var lists, generates []string
	for _, e := range f.events {
		if name, ok := strings.CutPrefix(e, "generate "); ok {
			generates = append(generates, name)
		} else if len(generates) > 0 {
			t.Errorf("%s after a call generated", e)
		} else {
			lists = append(lists, strings.TrimPrefix(e, "list "))
		}
	}
	if len(lists) != 6 || lists[0] != "global" || lists[1] != "us-east-1" {
		t.Errorf("listed %v, want global and us-east-1 first", lists)
	}
	if len(generates) != 6 || generates[0] != "global" || generates[1] != "us-east-1" {
		t.Errorf("generated %v, want global and us-east-1 first", generates)
	}
	for _, name := range []string{"us-east-1", "eu-west-1", "eu-central-1"} {
		if f.locks[name] != "lock of global" {
			t.Errorf("%s: lock %q, want the global call's", name, f.locks[name])
		}
		if len(f.roots[name]) != 6 {
			t.Errorf("%s: %d roots, want every call's", name, len(f.roots[name]))
		}
	}
	var paths []string
	for _, d := range run.report.Directories {
		paths = append(paths, d.Path)
	}
	if want := []string{"global", "us-east-1", "eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1"}; !slices.Equal(paths, want) {
		t.Errorf("directories %v, want the order of calls %v", paths, want)
	}
}

// The first call that fails cancels those running with it and starts no
// others; its error is the scope's, not the cancellation's. An interrupt
// shows as one.
func TestImportCallsCancelOnTheFirstFailure(t *testing.T) {
	run := NewRun()
	f := newFakeSteps()
	running := make(chan struct{})
	f.list = func(ctx context.Context, call string) error {
		switch call {
		case "eu-west-1":
			select {
			case <-running:
			case <-time.After(10 * time.Second):
				t.Error("eu-west-2 didn't start")
			}
			return errors.New("AccessDenied: the region is disabled")
		case "eu-west-2":
			close(running)
			<-ctx.Done()
			return ctx.Err()
		case "eu-west-3":
			t.Error("eu-west-3 started after a call failed")
		}
		return nil
	}
	err := importCalls(t.Context(), run, fakeCalls(Options{Out: "out", All: true, Parallel: 2}, "global", "eu-west-1", "eu-west-2", "eu-west-3"), f.steps())
	if err == nil || !strings.Contains(err.Error(), "call 2: AccessDenied") || errors.Is(err, context.Canceled) {
		t.Fatalf("want the failure of the second call, got %v", err)
	}
	for _, e := range f.events {
		if strings.HasPrefix(e, "generate ") {
			t.Errorf("%s after a call failed to list", e)
		}
	}
	if run.discovered["aws_vpc"] != 1 {
		t.Errorf("discovered %v, want the global call's", run.discovered)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	f = newFakeSteps()
	f.list = func(ctx context.Context, _ string) error { return ctx.Err() }
	if err := importCalls(ctx, NewRun(), fakeCalls(Options{Out: "out", All: true, Parallel: 2}, "global", "eu-west-1"), f.steps()); !errors.Is(err, context.Canceled) {
		t.Errorf("interrupted: %v", err)
	}
}

// The regions of accounts imported at once share --parallel with them: no
// more than that many calls run at once in all.
func TestImportCallsShareParallelWithAccounts(t *testing.T) {
	activeRun = NewRun()
	defer func() { activeRun = nil }()
	f := newFakeSteps()
	var running, most atomic.Int32
	f.list = func(context.Context, string) error {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		return nil
	}
	options := Options{Out: t.TempDir(), All: true, Parallel: 2}
	err := ImportEach(options, "account", []string{"111", "222", "333"}, func(o Options, account string) error {
		return importCalls(t.Context(), o.run, fakeCalls(o, account+"-global", account+"-eu-west-1", account+"-eu-west-2"), f.steps())
	})
	if err != nil {
		t.Fatal(err)
	}
	if most.Load() > 2 {
		t.Errorf("%d calls at once, want at most 2", most.Load())
	}
	if len(activeRun.report.Directories) != 9 || activeRun.discovered["aws_vpc"] != 9 {
		t.Errorf("directories %v, discovered %v", activeRun.report.Directories, activeRun.discovered)
	}
}
