// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/spf13/cobra"
)

// importEach imports each of scopes, the accounts or projects (kind) a
// command covers, with importOne, options.Parallel at a time, into the
// command's run. A scope that can't be imported, such as an account whose
// role can't be assumed, is recorded as a failure of the run and the others
// go on; an interrupt stops the run.
//
// In parallel, the first scope runs alone: it installs Terraform and the
// provider into the caches every root shares, and its lock file pins the
// provider for the others. Each scope then records into a run of its own
// (see engineRun.child), merged into the command's run in the order of
// scopes, so the report doesn't depend on which finished first. Scopes don't
// share API quotas: AWS throttles each account, and Google each project, on
// its own.
func importEach(options ImportOptions, kind string, scopes []string, importOne func(ImportOptions, string) error) error {
	run := activeRun
	if run == nil || options.Parallel <= 1 || len(scopes) < 2 {
		for _, scope := range scopes {
			err := importOne(options, scope)
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
		if _, err := run.selectionFile(options.Selection); err != nil {
			return err
		}
	}
	runs := make([]*engineRun, len(scopes))
	errs := make([]error, len(scopes))
	var interrupted atomic.Bool
	importAt := func(i int) {
		runs[i] = run.child()
		scopeOptions := options
		scopeOptions.run = runs[i]
		errs[i] = importOne(scopeOptions, scopes[i])
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

// parallelFlag adds --parallel to cmd: how many of what (accounts or
// projects) to import at once (see importEach).
func parallelFlag(cmd *cobra.Command, options *ImportOptions, what string) {
	cmd.PersistentFlags().IntVar(&options.Parallel, "parallel", 1, "how many "+what+" to import at once; each runs its own Terraform, so memory use grows with it")
}
