// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Call is one of the Import calls that cover a scope (see ImportCalls):
// what Import is passed, with what the log says about it.
type Call struct {
	Provider harvest.Provider
	Options  Options
	Args     []string
	// Where says what the call covers, such as "region eu-west-1", for the
	// log; empty logs nothing.
	Where string
	// First makes the call go before the calls that run at once, one at a
	// time, as AWS's global and us-east-1 calls do. The calls it applies
	// to come first.
	First bool
}

// ImportCalls makes the Import calls that cover one scope, an account,
// subscription or project, into the command's run: it lists every call
// first, then generates each call's roots, so that each root refers to the
// roots of every other call of the scope (see scopeRoots), as a regional
// root of an AWS account to its global root's IAM roles, and the global
// root to a regional bucket. Another scope's roots are never referred to:
// its resources are read with other credentials.
//
// Both steps go call by call: the first call, and the First calls after
// it, one at a time, as the first installs Terraform and the provider into
// the caches every root shares and its lock file pins the provider for the
// others; then the rest, options.Parallel at a time, over every scope of
// the command (see shared.callSlots). Each call records into a run of its
// own (see Run.child), merged into the command's run in the order of
// calls, so the report doesn't depend on which finished first; each logs
// when it starts and finishes listing, and generating its roots. The first
// call that fails stops the others, and its error is the scope's; nothing
// is generated after a call fails to list.
func ImportCalls(ctx context.Context, calls []Call) error {
	if len(calls) == 0 {
		return nil
	}
	options := calls[0].Options
	switch options.Engine {
	case "", EngineTerraform, EngineTofu:
	default:
		return fmt.Errorf("unknown --engine %q: use %s or %s", options.Engine, EngineTerraform, EngineTofu)
	}
	if len(calls) == 1 {
		calls[0].logWhere()
		return importWithEngine(ctx, calls[0].Provider, options, calls[0].Args)
	}
	if err := checkImportOptions(options); err != nil {
		return err
	}
	if err := checkSelectionOptions(options); err != nil {
		return err
	}
	steps := callSteps{
		list: func(ctx context.Context, run *Run, c Call) (*listedCall, error) {
			return listCall(ctx, run, c.Provider, c.Options, c.Args)
		},
		generate: func(ctx context.Context, c *listedCall, roots map[string][]engine.Import) error {
			return c.generate(ctx, roots)
		},
	}
	// As importWithEngine does: a command's calls share its run, and calls
	// on their own finish their own.
	run := options.run
	if run == nil {
		run = activeRun
	}
	if run == nil {
		run = NewRun()
		ctx, stop := InterruptContext(ctx)
		defer stop()
		if err := run.lockOut(options); err != nil {
			return run.finish(err)
		}
		return run.finish(Interrupted(ctx, importCalls(ctx, run, calls, steps)))
	}
	if err := run.lockOut(options); err != nil {
		return err
	}
	return importCalls(ctx, run, calls, steps)
}

// logWhere logs what the call covers, if Where says.
func (c Call) logWhere() {
	c.logStep("importing resources")
}

// logStep logs that the call got to step, such as "listed resources", in
// what it covers, if Where says: with calls running at once, the log
// tells when each starts and finishes listing and generating.
func (c Call) logStep(step string) {
	if c.Where != "" {
		log.Printf("%s: %s in %s", c.Provider.Name(), step, c.Where)
	}
}

// callSteps are the two steps of an Import call ImportCalls takes, which
// tests replace: listing it into a run, and generating its roots with
// those of the scope's other calls.
type callSteps struct {
	list     func(ctx context.Context, run *Run, c Call) (*listedCall, error)
	generate func(ctx context.Context, c *listedCall, roots map[string][]engine.Import) error
}

// importCalls makes calls into run, as ImportCalls says, with steps.
func importCalls(ctx context.Context, run *Run, calls []Call, steps callSteps) error {
	options := calls[0].Options
	if !run.used {
		// Should the calls fail before any records, the run still writes
		// its report (see Run.finish).
		run.options = options
	}
	if !options.Discover {
		// Loaded before the calls share it.
		if _, err := run.selectionFile(options); err != nil {
			return err
		}
	}
	runs := make([]*Run, len(calls))
	for i := range runs {
		runs[i] = run.child()
	}
	// Whatever happens, what each call did goes into the report, in the
	// order of calls.
	defer func() {
		for _, r := range runs {
			run.merge(r)
		}
	}()
	lead := 1
	for lead < len(calls) && calls[lead].First {
		lead++
	}
	slots := run.shared.callSlots(options.Parallel)
	name := func(i int) string {
		if calls[i].Where != "" {
			return calls[i].Where
		}
		return "call " + strconv.Itoa(i+1)
	}

	listed := make([]*listedCall, len(calls))
	err := eachCall(ctx, slots, len(calls), lead, options.Parallel, name, func(ctx context.Context, i int) error {
		calls[i].logWhere()
		var err error
		listed[i], err = steps.list(ctx, runs[i], calls[i])
		if err == nil {
			calls[i].logStep("listed resources")
		}
		return err
	})
	if err != nil || options.Discover {
		return err
	}
	// Before any root is generated: what goes from one to another is
	// worked out from where the roots had their resources before.
	if err := planHandovers(options.Out, listed); err != nil {
		return err
	}

	byDirs := make([]map[string][]engine.Import, len(calls))
	for i, c := range listed {
		if c != nil {
			byDirs[i] = c.byDir
		}
	}
	// The first lock file seeds the calls after it, as one run's calls
	// share it (see generateDir). Only the calls that go one at a time set
	// it, before the others start.
	seed := run.lock
	// Once every root is generated, or the calls stopped: what a root that
	// failed left half handed over.
	defer warnIncomplete(run, listed)
	return eachCall(ctx, slots, len(calls), lead, options.Parallel, name, func(ctx context.Context, i int) error {
		if listed[i] == nil {
			return nil
		}
		if runs[i].lock == nil {
			runs[i].lock = seed
		}
		// A call that found nothing has no roots to log.
		roots := len(listed[i].byDir) > 0
		if roots {
			calls[i].logStep("generating the roots")
		}
		err := steps.generate(ctx, listed[i], scopeRoots(byDirs, i))
		if i < lead && seed == nil {
			seed = runs[i].lock
		}
		if err == nil && roots {
			calls[i].logStep("generated the roots")
		}
		return err
	})
}

// eachCall runs do for 0 to n-1: the first lead of them one at a time, in
// order, then the others, up to parallel at once (one below 1), each
// holding one of slots while it runs. The first that fails, or panics,
// cancels the context the others run with, and none start after it; its
// error is returned. An interrupt of ctx shows in the error.
func eachCall(ctx context.Context, slots chan struct{}, n, lead, parallel int, name func(int) string, do func(context.Context, int) error) error {
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	var first error
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if first == nil {
			first = err
			cancel()
		}
	}
	failed := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return first != nil
	}
	at := func(i int) {
		select {
		case slots <- struct{}{}:
		case <-callCtx.Done():
			fail(callCtx.Err())
			return
		}
		defer func() { <-slots }()
		err := importSafely(func(Options, string) error { return do(callCtx, i) }, Options{}, name(i))
		if err != nil {
			fail(fmt.Errorf("%s: %w", name(i), err))
		}
	}

	for i := 0; i < min(lead, n) && !failed(); i++ {
		at(i)
	}
	started := make(chan struct{}, max(parallel, 1))
	var wg sync.WaitGroup
	for i := lead; i < n && !failed(); i++ {
		started <- struct{}{}
		if failed() {
			<-started
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-started }()
			at(i)
		}()
	}
	wg.Wait()

	if err := ctx.Err(); first != nil && err != nil && !errors.Is(first, err) {
		// What failed because of the interrupt still reads as one.
		return fmt.Errorf("%w: %w", err, first)
	}
	return first
}
