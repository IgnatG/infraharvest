// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"fmt"
	"log"

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
}

// ImportCalls makes the Import calls that cover one scope, an account,
// subscription or project, into the command's run: it lists every call
// first, then generates each call's roots, so that each root refers to the
// roots of every other call of the scope (see scopeRoots), as a regional
// root of an AWS account to its global root's IAM roles, and the global
// root to a regional bucket. Another scope's roots are never referred to:
// its resources are read with other credentials.
//
// Each call records into a run of its own (see Run.child), merged into the
// command's run in the order of calls. The first call that fails stops the
// others, and its error is the scope's; nothing is generated after a call
// fails to list.
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
	if c.Where != "" {
		log.Printf("%s: importing resources in %s", c.Provider.Name(), c.Where)
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
		if _, err := run.selectionFile(options.Selection); err != nil {
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

	listed := make([]*listedCall, len(calls))
	for i, c := range calls {
		c.logWhere()
		var err error
		if listed[i], err = steps.list(ctx, runs[i], c); err != nil {
			return err
		}
	}
	if options.Discover {
		return nil
	}

	byDirs := make([]map[string][]engine.Import, len(calls))
	for i, c := range listed {
		if c != nil {
			byDirs[i] = c.byDir
		}
	}
	// The first lock file seeds the calls after it, as one run's calls
	// share it (see generateDir).
	seed := run.lock
	for i, c := range listed {
		if c == nil {
			continue
		}
		if runs[i].lock == nil {
			runs[i].lock = seed
		}
		if err := steps.generate(ctx, c, scopeRoots(byDirs, i)); err != nil {
			return err
		}
		if seed == nil {
			seed = runs[i].lock
		}
	}
	return nil
}
