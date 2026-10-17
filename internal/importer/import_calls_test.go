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
	"testing"

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
