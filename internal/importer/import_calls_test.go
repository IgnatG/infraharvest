// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"bytes"
	"context"
	"errors"
	"log"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// fakeSteps list and generate calls as listCall and generate do, in
// short: each call, named by its first argument, lists one resource into a
// root of its own. They record what happened, in order.
type fakeSteps struct {
	mu     sync.Mutex
	events []string
	roots  map[string]map[string][]rootfile.Import
	locks  map[string]string
	// list, if set, runs as each call lists, and its error fails it.
	list func(ctx context.Context, call string) error
}

func newFakeSteps() *fakeSteps {
	return &fakeSteps{roots: map[string]map[string][]rootfile.Import{}, locks: map[string]string{}}
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
			byDir := map[string][]rootfile.Import{filepath.Join("out", name): {{Type: "aws_vpc", ID: name, Name: name}}}
			return &listedCall{callRecorder: callRecorder{run: run, scope: "aws/111/" + name}, options: c.Options, byDir: byDir}, nil
		},
		generate: func(_ context.Context, c *listedCall, roots map[string][]rootfile.Import) error {
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

// fakeCommand returns a command with options whose plan is plan, and
// whose calls steps import. Its provider has the service vpc, which it
// lists unless options say others.
func fakeCommand(options Options, plan Plan, steps callSteps) Command {
	if len(options.Services) == 0 {
		options.Services = []string{"vpc"}
	}
	return Command{
		Options:  options,
		Provider: &servicesProvider{},
		Plan:     func(context.Context, Options) (Plan, error) { return plan, nil },
		steps:    steps,
	}
}

// oneScope is the plan of the one scope the credentials reach, covered by
// calls.
func oneScope(calls []Call) Plan {
	return Plan{Calls: func(string) []Call { return calls }}
}

// importCalls imports calls, those of one scope, into run, with options
// and steps.
func importCalls(ctx context.Context, t *testing.T, run *Run, options Options, calls []Call, steps callSteps) error {
	t.Helper()
	t.Cleanup(run.unlockOut)
	return run.Import(ctx, fakeCommand(options, oneScope(calls), steps))
}

// Every call of a scope lists before any generates, and each root then
// sees the roots of every other call, before and after it; the calls
// record in their order, and the first lock file seeds the others.
func TestImportListsAllThenGenerates(t *testing.T) {
	run := NewRun()
	f := newFakeSteps()
	options := Options{Out: t.TempDir(), All: true}
	if err := importCalls(t.Context(), t, run, options, fakeCalls(options, "global", "us-east-1", "eu-west-1"), f.steps()); err != nil {
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

// captureLog returns what the standard logger logs while the test runs,
// without its timestamps.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logged bytes.Buffer
	writer, flags := log.Writer(), log.Flags()
	log.SetOutput(&logged)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(writer)
		log.SetFlags(flags)
	})
	return &logged
}

// Each of several calls logs when it starts and finishes listing and
// generating, by what it covers; one call alone logs only that it starts.
func TestImportLogsSteps(t *testing.T) {
	for name, tc := range map[string]struct {
		calls []string
		want  []string
	}{
		"several": {
			calls: []string{"the default region", "region eu-west-1"},
			want: []string{
				"aws: importing resources in the default region",
				"aws: listed resources in the default region",
				"aws: importing resources in region eu-west-1",
				"aws: listed resources in region eu-west-1",
				"aws: generating the roots in the default region",
				"aws: generated the roots in the default region",
				"aws: generating the roots in region eu-west-1",
				"aws: generated the roots in region eu-west-1",
			},
		},
		"one": {
			calls: []string{"the default region"},
			want:  []string{"aws: importing resources in the default region"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			logged := captureLog(t)
			options := Options{Out: t.TempDir(), All: true}
			names := []string{"global", "eu-west-1"}[:len(tc.calls)]
			calls := fakeCalls(options, names...)
			for i, where := range tc.calls {
				calls[i].Provider, calls[i].Where = &namedProvider{name: "aws"}, where
			}
			calls[0].First = true
			if err := importCalls(t.Context(), t, NewRun(), options, calls, newFakeSteps().steps()); err != nil {
				t.Fatal(err)
			}
			if got := strings.Split(strings.TrimSpace(logged.String()), "\n"); !slices.Equal(got, tc.want) {
				t.Errorf("logged\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// A call that fails to list fails the scope: the calls after it don't
// list, nothing is generated, and what the calls before it found is still
// in the run.
func TestImportStopsAtTheFirstFailure(t *testing.T) {
	run := NewRun()
	f := newFakeSteps()
	f.list = func(_ context.Context, call string) error {
		if call == "eu-west-1" {
			return errors.New("AccessDenied: the region is disabled")
		}
		return nil
	}
	options := Options{Out: t.TempDir(), All: true}
	err := importCalls(t.Context(), t, run, options, fakeCalls(options, "global", "eu-west-1", "eu-west-2"), f.steps())
	if err == nil || !strings.Contains(err.Error(), "call 2: AccessDenied") {
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
func TestImportDiscovers(t *testing.T) {
	run := NewRun()
	f := newFakeSteps()
	options := Options{Out: t.TempDir(), Discover: true}
	if err := importCalls(t.Context(), t, run, options, fakeCalls(options, "global", "eu-west-1"), f.steps()); err != nil {
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
func TestImportScopeCallsInParallel(t *testing.T) {
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
	options := Options{Out: t.TempDir(), All: true, Parallel: 3}
	calls := fakeCalls(options, "global", "us-east-1", "eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1")
	calls[0].First, calls[1].First = true, true
	if err := importCalls(t.Context(), t, run, options, calls, f.steps()); err != nil {
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
func TestImportCancelsOnTheFirstFailure(t *testing.T) {
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
	options := Options{Out: t.TempDir(), All: true, Parallel: 2}
	err := importCalls(t.Context(), t, run, options, fakeCalls(options, "global", "eu-west-1", "eu-west-2", "eu-west-3"), f.steps())
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
	options.Out = t.TempDir()
	if err := importCalls(ctx, t, NewRun(), options, fakeCalls(options, "global", "eu-west-1"), f.steps()); !errors.Is(err, context.Canceled) {
		t.Errorf("interrupted: %v", err)
	}
}

// The regions of accounts imported at once share --parallel with them: no
// more than that many calls run at once in all.
func TestImportSharesParallelWithAccounts(t *testing.T) {
	run := NewRun()
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
	// After TempDir, so that the lock is let go before the directory goes.
	t.Cleanup(run.unlockOut)
	plan := Plan{Kind: "account", Scopes: []string{"111", "222", "333"}, Calls: func(account string) []Call {
		return fakeCalls(options, account+"-global", account+"-eu-west-1", account+"-eu-west-2")
	}}
	if err := run.Import(t.Context(), fakeCommand(options, plan, f.steps())); err != nil {
		t.Fatal(err)
	}
	if most.Load() > 2 {
		t.Errorf("%d calls at once, want at most 2", most.Load())
	}
	if len(run.report.Directories) != 9 || run.discovered["aws_vpc"] != 9 {
		t.Errorf("directories %v, discovered %v", run.report.Directories, run.discovered)
	}
}

// One call is imported as one of several is: it records the same into
// the command's run, in its own scope, with the command's options; only
// its error goes without the name of the call, as there is no other.
func TestImportOneCallAsOneOfSeveral(t *testing.T) {
	imported := func(t *testing.T, names ...string) (*Run, Options, error) {
		t.Helper()
		run := NewRun()
		f := newFakeSteps()
		f.list = func(_ context.Context, call string) error {
			if call == "failing" {
				return errors.New("AccessDenied")
			}
			return nil
		}
		options := Options{Out: t.TempDir(), All: true}
		err := importCalls(t.Context(), t, run, options, fakeCalls(options, names...), f.steps())
		return run, options, err
	}

	one, oneOptions, err := imported(t, "global")
	if err != nil {
		t.Fatal(err)
	}
	several, severalOptions, err := imported(t, "global", "eu-west-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(one.report.Directories) != 1 || !reflect.DeepEqual(one.report.Directories[0], several.report.Directories[0]) {
		t.Errorf("directories: one call %+v, of several %+v", one.report.Directories, several.report.Directories)
	}
	if len(one.report.Scopes) != 1 || !reflect.DeepEqual(one.report.Scopes[0], several.report.Scopes[0]) {
		t.Errorf("scopes: one call %+v, of several %+v", one.report.Scopes, several.report.Scopes)
	}
	if one.discovered["aws_vpc"] != 1 || several.discovered["aws_vpc"] != 2 || !one.used || !several.used {
		t.Errorf("discovered: one call %v, several %v", one.discovered, several.discovered)
	}
	if one.options.Out != oneOptions.Out || several.options.Out != severalOptions.Out {
		t.Errorf("options: one call %+v, several %+v", one.options, several.options)
	}

	if _, _, err := imported(t, "failing"); err == nil || err.Error() != "AccessDenied" {
		t.Errorf("one call: %v, want its error as it is", err)
	}
	if _, _, err := imported(t, "global", "failing"); err == nil || err.Error() != "call 2: AccessDenied" {
		t.Errorf("several calls: %v, want the call's error by its name", err)
	}
}

// The options are checked once, before anything is planned or listed,
// however many scopes the command covers: the error is the command's,
// and the run records nothing. --check stops once they are.
func TestImportChecksTheOptionsOnce(t *testing.T) {
	scopes := []string{"111", "222", "333"}
	for name, tc := range map[string]struct {
		options Options
		check   func(Options) error
		want    string
	}{
		"an unknown engine":   {options: Options{All: true, Engine: "tofu-ish"}, want: `unknown --engine "tofu-ish"`},
		"no selection":        {options: Options{}, want: "selection"},
		"the provider's own":  {options: Options{All: true}, check: func(Options) error { return errors.New("--accounts and --organization") }, want: "--accounts and --organization"},
		"merge, nothing to":   {options: Options{All: true, MergeEnvironments: true, MergeEnvironmentsChosen: true}, want: "--merge-environments merges"},
		"merge, by default":   {options: Options{All: true, MergeEnvironments: true}},
		"a negative parallel": {options: Options{All: true, Parallel: -1}, want: "--parallel can't be negative"},
	} {
		t.Run(name, func(t *testing.T) {
			var planned, listed atomic.Int32
			f := newFakeSteps()
			f.list = func(context.Context, string) error {
				listed.Add(1)
				return nil
			}
			tc.options.Out = t.TempDir()
			cmd := fakeCommand(tc.options, Plan{}, f.steps())
			cmd.Check = tc.check
			cmd.Plan = func(context.Context, Options) (Plan, error) {
				planned.Add(1)
				return Plan{Kind: "account", Scopes: scopes, Calls: func(scope string) []Call { return fakeCalls(tc.options, scope) }}, nil
			}
			run := NewRun()
			t.Cleanup(run.unlockOut)
			err := run.Import(t.Context(), cmd)
			if tc.want == "" {
				if err != nil || planned.Load() != 1 || listed.Load() != 3 {
					t.Fatalf("got %v, planned %d, listed %d", err, planned.Load(), listed.Load())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "account ") {
				t.Errorf("got %q, want %q once, of no account", err, tc.want)
			}
			if planned.Load() != 0 || listed.Load() != 0 || run.used || len(run.failures) != 0 {
				t.Errorf("planned %d, listed %d, used %v, failures %v", planned.Load(), listed.Load(), run.used, run.failures)
			}
		})
	}

	// --check: nothing is planned.
	cmd := fakeCommand(Options{Out: t.TempDir(), All: true}, Plan{}, newFakeSteps().steps())
	cmd.CheckOnly = true
	cmd.Plan = func(context.Context, Options) (Plan, error) {
		t.Error("planned with --check")
		return Plan{}, nil
	}
	if err := NewRun().Import(t.Context(), cmd); err != nil {
		t.Errorf("--check: %v", err)
	}
}

// checkOnly checks options as a provider command's --check does, with
// provider (see Run.Import).
func checkOnly(provider harvest.Provider, options Options) error {
	return NewRun().Import(context.Background(), Command{Options: options, Provider: provider, CheckOnly: true})
}

// A call alone that panics fails as an error, not a crash, and the run
// still writes its report; the log names the call by its scope, as a
// scope that panics is named, or else as one of several calls is.
func TestImportRecoversAPanicOfOneCall(t *testing.T) {
	for name, tc := range map[string]struct {
		scopes []string
		// logged are in the log; failure, if set, is the run's failure.
		logged  []string
		failure string
	}{
		"in a scope": {
			scopes:  []string{"sub-x"},
			logged:  []string{"sub-x panicked: unexpected response", "subscription sub-x: panic: unexpected response"},
			failure: "subscription sub-x: panic: unexpected response",
		},
		"of no scope": {
			logged: []string{"call 1 panicked: unexpected response"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			logged := captureLog(t)
			f := newFakeSteps()
			f.list = func(context.Context, string) error { panic("unexpected response") }
			options := Options{Out: t.TempDir(), All: true}
			plan := Plan{Kind: "subscription", Scopes: tc.scopes, Calls: func(string) []Call { return fakeCalls(options, "default") }}
			run := NewRun()
			t.Cleanup(run.unlockOut)
			var imported error
			err := run.Do(func() error {
				imported = run.Import(t.Context(), fakeCommand(options, plan, f.steps()))
				return imported
			})
			if err == nil {
				t.Fatal("want the run failed")
			}
			if tc.failure != "" {
				if imported != nil || len(run.failures) != 1 || !strings.HasPrefix(run.failures[0].Error(), tc.failure) {
					t.Errorf("import %v, failures %v, want %q recorded", imported, run.failures, tc.failure)
				}
			} else if imported == nil || !strings.HasPrefix(imported.Error(), "panic: unexpected response") {
				t.Errorf("import %v, want the panic as its error", imported)
			}
			for _, want := range tc.logged {
				if !strings.Contains(logged.String(), want) {
					t.Errorf("log %q, want %q", logged.String(), want)
				}
			}
			if _, err := os.Stat(filepath.Join(options.Out, report.Dir, "coverage.json")); err != nil {
				t.Errorf("no report: %v", err)
			}
		})
	}
}
