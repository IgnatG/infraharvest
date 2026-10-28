// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package discovery

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"runtime/pprof"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// fake lists one resource after delay, and returns it with err; with
// hang, it lists until its context is done, and returns it with err too.
type fake struct {
	name    string
	delay   time.Duration
	err     error
	hang    bool
	running *atomic.Int32
	most    *atomic.Int32
}

func (f *fake) Name() string { return f.name }

func (f *fake) Discover(ctx context.Context) ([]harvest.Resource, error) {
	if f.running != nil {
		n := f.running.Add(1)
		defer f.running.Add(-1)
		for {
			most := f.most.Load()
			if n <= most || f.most.CompareAndSwap(most, n) {
				break
			}
		}
	}
	listed := []harvest.Resource{harvest.RecordBare(f.name+"-1", f.name, "fake_thing", "fake")}
	if f.hang {
		<-ctx.Done()
		return listed, f.err
	}
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return listed, f.err
}

func TestListKeepsTheOrderAndTheBound(t *testing.T) {
	var running, most atomic.Int32
	var ds []Discoverer
	for i := range 8 {
		ds = append(ds, &fake{name: fmt.Sprint(i), delay: time.Duration(8-i) * 5 * time.Millisecond, running: &running, most: &most})
	}
	results, err := List(t.Context(), ds, Options{Parallel: 3})
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range results {
		if r.Name != fmt.Sprint(i) || r.Err != nil || len(r.Resources) != 1 {
			t.Errorf("result %d: %+v", i, r)
		}
	}
	if got := most.Load(); got > 3 || got < 2 {
		t.Errorf("%d discoverers ran at once, want up to 3", got)
	}
}

// A discoverer that fails has what it found left out; one that fails in
// part, or whose deadline passes, keeps it, and fails in part.
// The clock is virtual (synctest), so the good discoverers finish within
// the deadline however loaded the runner.
func TestListReportsFailuresAndDeadlines(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ds := []Discoverer{
			&fake{name: "good"},
			&fake{name: "bad", err: errors.New("access denied")},
			&fake{name: "slow", hang: true},
			&fake{name: "partial", err: harvest.Partial(errors.New("a part was refused"))},
			&fake{name: "slow partial", hang: true, err: harvest.Partial(errors.New("a part was refused"))},
		}
		results, err := List(t.Context(), ds, Options{Timeout: 10 * time.Millisecond, Parallel: 5})
		if err != nil {
			t.Fatal(err)
		}
		if results[0].Err != nil || len(results[0].Resources) != 1 {
			t.Errorf("good: %+v", results[0])
		}
		if results[1].Err == nil || results[1].Err.Error() != "access denied" || len(results[1].Resources) != 0 {
			t.Errorf("bad: %+v", results[1])
		}
		if !errors.Is(results[2].Err, ErrDeadline) || !harvest.IsPartial(results[2].Err) || len(results[2].Resources) != 1 {
			t.Errorf("slow: %+v", results[2])
		}
		if !harvest.IsPartial(results[3].Err) || len(results[3].Resources) != 1 {
			t.Errorf("partial: %+v", results[3])
		}
		if !errors.Is(results[4].Err, ErrDeadline) || !harvest.IsPartial(results[4].Err) || len(results[4].Resources) != 1 {
			t.Errorf("slow partial: %+v", results[4])
		}
	})
}

func TestListStopsWhenTheContextIsDone(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := List(ctx, []Discoverer{&fake{name: "slow", hang: true}, &fake{name: "good"}}, Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}

// labelled records the pprof labels its context carries.
type labelled struct {
	name   string
	labels map[string]string
}

func (l *labelled) Name() string { return l.name }

func (l *labelled) Discover(ctx context.Context) ([]harvest.Resource, error) {
	l.labels = contextLabels(ctx)
	return nil, nil
}

func contextLabels(ctx context.Context) map[string]string {
	labels := map[string]string{}
	pprof.ForLabels(ctx, func(key, value string) bool {
		labels[key] = value
		return true
	})
	return labels
}

// listerFunc is a lister that lists with a function.
type listerFunc func(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error)

func (f listerFunc) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return f(ctx, s)
}

// listerProvider makes every service's lister the one it has.
type listerProvider struct {
	harvest.Provider
	lister harvest.Lister
	err    error
}

func (p *listerProvider) Name() string { return "fake" }
func (p *listerProvider) Lister(string) (harvest.Lister, error) {
	return p.lister, p.err
}

func TestListLabelsTheDiscoverers(t *testing.T) {
	t.Parallel()
	plain := &labelled{name: "plain"}
	var labels map[string]string
	provider := &listerProvider{lister: listerFunc(func(ctx context.Context, _ harvest.Settings) ([]harvest.Resource, error) {
		labels = contextLabels(ctx)
		return nil, nil
	})}
	ds := []Discoverer{plain, Service("compute", provider, harvest.Settings{})}
	if _, err := List(t.Context(), ds, Options{Parallel: 2}); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"service": "plain"}; !maps.Equal(plain.labels, want) {
		t.Errorf("a discoverer: got labels %v, want %v", plain.labels, want)
	}
	if want := map[string]string{"provider": "fake", "service": "compute"}; !maps.Equal(labels, want) {
		t.Errorf("a service: got labels %v, want %v", labels, want)
	}
}

// listsOne is a lister that lists one resource and returns err.
func listsOne(err error) listerFunc {
	return func(context.Context, harvest.Settings) ([]harvest.Resource, error) {
		return []harvest.Resource{harvest.RecordBare("id-1", "one", "fake_thing", "fake")}, err
	}
}

// A service whose lister fails in part returns what it listed with the
// error; one whose lister fails returns nothing.
func TestServiceKeepsWhatAPartialListingFound(t *testing.T) {
	t.Parallel()
	refused := errors.New("a part was refused")
	resources, err := Service("dns", &listerProvider{lister: listsOne(harvest.Partial(refused))}, harvest.Settings{}).Discover(t.Context())
	if !harvest.IsPartial(err) || !errors.Is(err, refused) || len(resources) != 1 {
		t.Errorf("partial: got %d resources and %v, want 1 and the partial error", len(resources), err)
	}
	resources, err = Service("dns", &listerProvider{lister: listsOne(refused)}, harvest.Settings{}).Discover(t.Context())
	if !errors.Is(err, refused) || resources != nil {
		t.Errorf("failed: got %d resources and %v, want none and the error", len(resources), err)
	}
	// A lister whose calls fail as its context is done, at the deadline,
	// listed in part.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	resources, err = Service("dns", &listerProvider{lister: listsOne(context.Canceled)}, harvest.Settings{}).Discover(ctx)
	if !harvest.IsPartial(err) || !errors.Is(err, context.Canceled) || len(resources) != 1 {
		t.Errorf("stopped: got %d resources and %v, want 1 and a partial error", len(resources), err)
	}
}

// A service that can't make its lister fails with the provider's error.
func TestServiceFailsWithoutALister(t *testing.T) {
	t.Parallel()
	unknown := errors.New("not a service")
	resources, err := Service("nothing", &listerProvider{err: unknown}, harvest.Settings{}).Discover(t.Context())
	if !errors.Is(err, unknown) || resources != nil {
		t.Errorf("got %d resources and %v, want none and the provider's error", len(resources), err)
	}
}

// The lister is given the import's settings with the service's and the
// provider's names, and what it lists is pruned once, whatever the lister
// does: the ID filters apply and duplicates go.
func TestServiceGivesSettingsAndPrunes(t *testing.T) {
	t.Parallel()
	var got harvest.Settings
	provider := &listerProvider{lister: listerFunc(func(_ context.Context, s harvest.Settings) ([]harvest.Resource, error) {
		got = s
		return []harvest.Resource{
			harvest.RecordBare("vpc-1", "one", "fake_vpc", "fake"),
			harvest.RecordBare("vpc-2", "two", "fake_vpc", "fake"),
			harvest.RecordBare("vpc-1", "one", "fake_vpc", "fake"),
		}, nil
	})}
	filters := []harvest.Filter{{Service: "vpc", Path: "id", Values: []string{"vpc-1"}}}
	settings := harvest.Settings{Service: "other", Provider: "other", Verbose: true, Filters: filters, Unwanted: func(typ string) bool { return typ == "fake_user" }}

	resources, err := Service("vpc", provider, settings).Discover(t.Context())

	if err != nil {
		t.Fatal(err)
	}
	if got.Service != "vpc" || got.Provider != "fake" || !got.Verbose || !reflect.DeepEqual(got.Filters, filters) || got.Wants("fake_user") || !got.Wants("fake_vpc") {
		t.Errorf("settings: got %+v", got)
	}
	if len(resources) != 1 || resources[0].Observed.ID != "vpc-1" {
		t.Errorf("resources: got %v, want vpc-1 once", resources)
	}
}

// panicking panics when it lists.
type panicking struct{}

func (panicking) Name() string { return "broken" }

func (panicking) Discover(context.Context) ([]harvest.Resource, error) {
	panic("unexpected response")
}

// A discoverer that panics fails as its service, and the others list.
func TestListRecoversAPanic(t *testing.T) {
	t.Parallel()
	results, err := List(t.Context(), []Discoverer{panicking{}, &fake{name: "good"}}, Options{Parallel: 2})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Name != "broken" || results[0].Err == nil || !strings.Contains(results[0].Err.Error(), "panic:") || results[0].Resources != nil {
		t.Errorf("the panicking service: %+v", results[0])
	}
	if results[1].Err != nil || len(results[1].Resources) != 1 {
		t.Errorf("the other service: %+v", results[1])
	}
}
