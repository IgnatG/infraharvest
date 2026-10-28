// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package discovery lists the resources an import covers: one Discoverer
// per service and scope (an account and region, a subscription, a
// project), run by List with a deadline each and a bounded number at a
// time. Every API call a discoverer makes takes the context it is given,
// and goes through a rate limit per API (see package ratelimit).
package discovery

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Discoverer lists the resources of one service in one scope.
type Discoverer interface {
	// Name is the service's name, as failures name it.
	Name() string
	// Discover lists the service's resources. Every API call it makes
	// takes ctx, with its deadline and cancellation. With a partial error
	// (see harvest.Partial) it returns what it listed too.
	Discover(ctx context.Context) ([]harvest.Resource, error)
}

// ErrDeadline is the error of a discoverer that was still listing when
// its deadline passed: what it found may be incomplete. List returns it
// as partial (see harvest.Partial), with what was found.
var ErrDeadline = errors.New("listing took longer than its deadline; its resources may be incomplete")

// Options bound a listing.
type Options struct {
	// Timeout is the longest each discoverer lists; 0 means no limit.
	Timeout time.Duration
	// Parallel is how many discoverers list at once; below 1, one.
	Parallel int
}

// Result is what one discoverer found.
type Result struct {
	Name      string
	Resources []harvest.Resource
	// Err is why the discoverer failed: what it found is left out, unless
	// Err is partial (see harvest.IsPartial), as when its deadline passed,
	// when Resources holds what it listed.
	Err error
}

// List runs the discoverers, at most opts.Parallel at a time and each
// within opts.Timeout, and returns their results in the discoverers'
// order. A discoverer whose deadline passed fails in part with
// ErrDeadline, whatever it returned, as listers that log a failed call
// and go on return no error, and keeps what it found by then: a long
// listing of a large estate isn't lost for its last minutes. A
// discoverer that fails with a partial error keeps what it found too.
// Either is a failure of the import all the same, after which what is
// gone from the cloud is unknown. Once ctx is done List starts no more
// discoverers, waits for those running, and returns ctx's error.
func List(ctx context.Context, discoverers []Discoverer, opts Options) ([]Result, error) {
	parallel := max(opts.Parallel, 1)
	results := make([]Result, len(discoverers))
	slots := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, d := range discoverers {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			// The service label names the discoverer in profiles and in a
			// panic's traceback, which shows goroutine labels.
			pprof.Do(ctx, pprof.Labels("service", d.Name()), func(ctx context.Context) {
				results[i] = discover(ctx, d, opts.Timeout)
			})
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// safely runs d, turning a panic into its error, with the stack in the
// log: a lister's bug fails its service, as any other failure does, and
// the rest of the run goes on.
func safely(ctx context.Context, d Discoverer) (resources []harvest.Resource, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("service %s panicked: %v\n%s", d.Name(), r, debug.Stack())
			resources, err = nil, fmt.Errorf("panic: %v (the log has the stack)", r)
		}
	}()
	return d.Discover(ctx)
}

// discover runs one discoverer within timeout.
func discover(ctx context.Context, d Discoverer, timeout time.Duration) Result {
	var listCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		listCtx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		listCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	resources, err := safely(listCtx, d)
	if ctx.Err() == nil && listCtx.Err() != nil {
		err = harvest.Partial(ErrDeadline)
	}
	if err != nil && !harvest.IsPartial(err) {
		return Result{Name: d.Name(), Err: err}
	}
	return Result{Name: d.Name(), Resources: resources, Err: err}
}
