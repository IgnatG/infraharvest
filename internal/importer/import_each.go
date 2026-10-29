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

// importEach imports each of scopes, the accounts or projects (kind) a
// command covers, with importOne, options.Parallel at a time, into r, the
// command's run, which importOne is passed. A scope that can't be
// imported, such as an account whose role can't be assumed, is recorded as
// a failure of the run and the others go on; an interrupt stops the run.
//
// In parallel, the first scope runs alone: it installs Terraform and the
// provider into the caches every root shares, and its lock file pins the
// provider for the others. Each scope then records into a run of its own
// (see Run.child), which importOne is passed in place of r, merged into
// the command's run in the order of scopes, so the report doesn't depend
// on which finished first. AWS throttles each account on its own, and
// each gets a rate limit of its own; Google charges most quotas to the credentials' quota project, which
// every project of a run shares, so Google's rate limit is shared too.
func (r *Run) importEach(options Options, kind string, scopes []string, importOne func(*Run, string) error) error {
	r.shared.scopes = scopes
	if options.Parallel <= 1 || len(scopes) < 2 {
		for _, scope := range scopes {
			err := importSafely(scope, func() error { return importOne(r, scope) })
			if err == nil {
				continue
			}
			if errors.Is(err, context.Canceled) {
				return err
			}
			log.Printf("%s %s: %v", kind, scope, err)
			r.recordFailure(options, fmt.Errorf("%s %s: %w", kind, scope, err))
		}
		return nil
	}

	if !options.Discover {
		// Loaded before the scopes share it.
		if _, err := r.selectionFile(options); err != nil {
			return err
		}
	}
	runs := make([]*Run, len(scopes))
	errs := make([]error, len(scopes))
	var interrupted atomic.Bool
	importAt := func(i int) {
		runs[i] = r.child()
		errs[i] = importSafely(scopes[i], func() error { return importOne(runs[i], scopes[i]) })
		if errors.Is(errs[i], context.Canceled) {
			interrupted.Store(true)
		}
	}

	importAt(0)
	if r.lock == nil {
		r.lock = runs[0].lock
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
		r.merge(runs[i])
		switch err := errs[i]; {
		case err == nil:
		case errors.Is(err, context.Canceled):
			canceled = err
		default:
			log.Printf("%s %s: %v", kind, scope, err)
			r.recordFailure(options, fmt.Errorf("%s %s: %w", kind, scope, err))
		}
	}
	return canceled
}

// importSafely runs do, which imports what name names, turning a panic
// into its error, with the stack in the log: the scope or call fails, as
// one whose role can't be assumed does, and the others go on, so the run
// still ends with a report.
func importSafely(name string, do func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("%s panicked: %v\n%s", name, r, debug.Stack())
			err = fmt.Errorf("panic: %v (the log has the stack)", r)
		}
	}()
	return do()
}
