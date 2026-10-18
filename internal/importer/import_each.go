// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"sync/atomic"
)

// ImportEach imports each of scopes, the accounts or projects (kind) a
// command covers, with importOne, options.Parallel at a time, into the
// command's run. A scope that can't be imported, such as an account whose
// role can't be assumed, is recorded as a failure of the run and the others
// go on; an interrupt stops the run.
//
// In parallel, the first scope runs alone: it installs Terraform and the
// provider into the caches every root shares, and its lock file pins the
// provider for the others. Each scope then records into a run of its own
// (see Run.child), merged into the command's run in the order of
// scopes, so the report doesn't depend on which finished first. AWS
// throttles each account on its own, and each gets a rate limit of its
// own; Google charges most quotas to the credentials' quota project, which
// every project of a run shares, so Google's rate limit is shared too.
func ImportEach(options Options, kind string, scopes []string, importOne func(Options, string) error) error {
	run := activeRun
	if run == nil || options.Parallel <= 1 || len(scopes) < 2 {
		for _, scope := range scopes {
			err := importSafely(importOne, options, scope)
			if err == nil {
				continue
			}
			if run == nil || errors.Is(err, context.Canceled) {
				return err
			}
			log.Printf("%s %s: %v", kind, scope, err)
			run.recordFailure(options, fmt.Errorf("%s %s: %w", kind, scope, err))
		}
		return nil
	}

	if !options.Discover {
		// Loaded before the scopes share it.
		if _, err := run.selectionFile(options); err != nil {
			return err
		}
	}
	runs := make([]*Run, len(scopes))
	errs := make([]error, len(scopes))
	var interrupted atomic.Bool
	importAt := func(i int) {
		runs[i] = run.child()
		scopeOptions := options
		scopeOptions.run = runs[i]
		errs[i] = importSafely(importOne, scopeOptions, scopes[i])
		if errors.Is(errs[i], context.Canceled) {
			interrupted.Store(true)
		}
	}

	importAt(0)
	if run.lock == nil {
		run.lock = runs[0].lock
	}
	slots := make(chan struct{}, options.Parallel)
	var wg sync.WaitGroup
	for i := 1; i < len(scopes) && !interrupted.Load(); i++ {
		slots <- struct{}{}
		if interrupted.Load() {
			<-slots
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			importAt(i)
		}()
	}
	wg.Wait()

	var canceled error
	for i, scope := range scopes {
		if runs[i] == nil {
			continue // not started: interrupted
		}
		run.merge(runs[i])
		switch err := errs[i]; {
		case err == nil:
		case errors.Is(err, context.Canceled):
			canceled = err
		default:
			log.Printf("%s %s: %v", kind, scope, err)
			run.recordFailure(options, fmt.Errorf("%s %s: %w", kind, scope, err))
		}
	}
	return canceled
}

// importSafely imports scope with importOne, turning a panic into its
// error, with the stack in the log: the scope fails, as one whose role
// can't be assumed does, and the others go on, so the run still ends with
// a report.
func importSafely(importOne func(Options, string) error, options Options, scope string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("%s panicked: %v\n%s", scope, r, debug.Stack())
			err = fmt.Errorf("panic: %v (the log has the stack)", r)
		}
	}()
	return importOne(options, scope)
}
