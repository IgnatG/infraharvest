// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package discovery

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"runtime/pprof"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// fake lists one resource after delay, or fails with err; with hang, it
// lists until its context is done, and returns no error.
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
	if f.hang {
		<-ctx.Done()
		return nil, nil
	}
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	return []harvest.Resource{harvest.RecordBare(f.name+"-1", f.name, "fake_thing", "fake")}, nil
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

func TestListReportsFailuresAndDeadlines(t *testing.T) {
	ds := []Discoverer{
		&fake{name: "good"},
		&fake{name: "bad", err: errors.New("access denied")},
		&fake{name: "slow", hang: true},
	}
	results, err := List(t.Context(), ds, Options{Timeout: 10 * time.Millisecond, Parallel: 3})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Err != nil || len(results[0].Resources) != 1 {
		t.Errorf("good: %+v", results[0])
	}
	if results[1].Err == nil || results[1].Err.Error() != "access denied" {
		t.Errorf("bad: %+v", results[1])
	}
	if !errors.Is(results[2].Err, ErrDeadline) {
		t.Errorf("slow: %+v", results[2])
	}
}

func TestListStopsWhenTheContextIsDone(t *testing.T) {
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

// labelProvider's service records the pprof labels of the context it
// lists with.
type labelProvider struct {
	harvest.Provider
	service *labelService
}

func (p *labelProvider) Name() string             { return "fake" }
func (p *labelProvider) Selected() harvest.Lister { return p.service }
func (p *labelProvider) Select(name string, _ bool) error {
	p.service = &labelService{}
	p.service.Configure(harvest.ListerConfig{Service: name})
	return nil
}

type labelService struct {
	harvest.ListerBase
	labels map[string]string
}

func (s *labelService) List() error {
	s.labels = contextLabels(s.Context())
	return nil
}

func TestListLabelsTheDiscoverers(t *testing.T) {
	plain := &labelled{name: "plain"}
	provider := &labelProvider{}
	ds := []Discoverer{plain, Service("compute", provider, false, nil)}
	if _, err := List(t.Context(), ds, Options{Parallel: 2}); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"service": "plain"}; !maps.Equal(plain.labels, want) {
		t.Errorf("a discoverer: got labels %v, want %v", plain.labels, want)
	}
	if want := map[string]string{"provider": "fake", "service": "compute"}; !maps.Equal(provider.service.labels, want) {
		t.Errorf("a service: got labels %v, want %v", provider.service.labels, want)
	}
}
