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

	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// Command is what one provider command imports: its options, as its
// flags and the configuration file set them, and how they expand into the
// scopes to import and the Import calls that cover each (see Run.Import).
type Command struct {
	Options Options
	// Provider is a provider of the command's cloud, which the options are
	// checked against (see checkOptions).
	Provider harvest.Provider
	// Check rejects what the provider's own flags say that no plan could
	// do, without reaching the cloud; nil checks nothing more.
	Check func(options Options) error
	// CheckOnly stops once the options are checked, as --check does.
	CheckOnly bool
	// Plan expands the checked options into the scopes to import, before
	// anything is imported. Its context carries the run's limiter (see
	// Run.withLimiter).
	Plan func(ctx context.Context, options Options) (Plan, error)
	// steps are those of each Import call, which tests replace; zero lists
	// and generates (see engineSteps).
	steps callSteps
}

// Plan is what a command imports: the scopes, accounts, subscriptions or
// projects, and the Import calls that cover each.
type Plan struct {
	// Kind names one of Scopes in logs and failures: "account",
	// "subscription" or "project".
	Kind string
	// Scopes are the accounts, subscriptions or projects to import, each
	// apart (see Run.importEach). Without any, Calls covers the one scope
	// the credentials reach, and is passed "" for it.
	Scopes []string
	// Calls returns the Import calls that cover one scope, in order.
	Calls func(scope string) []Call
}

// Call is one of the Import calls that cover a scope (see
// Run.importScope): the provider, options and args of one, with what the
// log says about it.
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

// Import runs cmd into r, the command's run, which the caller finishes
// (see Run.Do): it checks the options, once, whatever the command covers:
// --merge-environments against the configuration file's environments
// (see config.CheckMergeEnvironments), the --policy directories (see
// resolvePolicies) and the rest (see checkOptions), then the provider's own flags, and with CheckOnly
// stops there; else it plans the scopes, imports each (see
// Run.importScope), and merges the roots of each stack's environments
// (see Run.mergeEnvironments). A scope that fails is recorded and the
// others go on (see Run.importEach).
func (r *Run) Import(ctx context.Context, cmd Command) error {
	options := cmd.Options
	if err := config.CheckMergeEnvironments(r.environments, options.MergeEnvironments, options.MergeEnvironmentsChosen); err != nil {
		return err
	}
	// Discover runs no gate, and takes --policy only as the configuration
	// file it shares with import may set it.
	if !options.Discover {
		policies, err := resolvePolicies(options.Policies)
		if err != nil {
			return err
		}
		options.Policies = policies
	}
	if err := checkOptions(cmd.Provider, options); err != nil {
		return err
	}
	if cmd.Check != nil {
		if err := cmd.Check(options); err != nil {
			return err
		}
	}
	if cmd.CheckOnly {
		return nil
	}
	// The scopes are planned in the run, so that what the providers
	// resolve to plan them, such as the credentials, serves the Import
	// calls too.
	plan, err := cmd.Plan(r.withLimiter(ctx, options.RateLimit), options)
	if err != nil {
		return err
	}
	steps := cmd.steps
	if steps.list == nil {
		steps = engineSteps
	}
	importScope := func(scopeRun *Run, scope string) error {
		return scopeRun.importScope(ctx, options, plan.Calls(scope), steps)
	}
	if plan.Scopes == nil {
		err = importScope(r, "")
	} else {
		err = r.importEach(options, plan.Kind, plan.Scopes, importScope)
	}
	if err != nil {
		return err
	}
	// Once every scope is imported: the environments of a stack may be in
	// different accounts, subscriptions or projects.
	return r.mergeEnvironments(ctx)
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

// callSteps are the two steps of an Import call, which tests replace:
// listing it into a run, and generating its roots with those of the
// scope's other calls.
type callSteps struct {
	list     func(ctx context.Context, run *Run, c Call) (*listedCall, error)
	generate func(ctx context.Context, c *listedCall, roots map[string][]rootfile.Import) error
}

// engineSteps list a call (see listCall) and generate its roots with the
// engine (see listedCall.generate).
var engineSteps = callSteps{
	list: func(ctx context.Context, run *Run, c Call) (*listedCall, error) {
		return listCall(ctx, run, c.Provider, c.Options, c.Args)
	},
	generate: func(ctx context.Context, c *listedCall, roots map[string][]rootfile.Import) error {
		return c.generate(ctx, roots)
	},
}

// importScope makes calls, the Import calls that cover one scope, an
// account, subscription or project, into r, the scope's run, with steps
// and the command's options: it lists every call first, then generates
// each call's roots, so that each root refers to the roots of every other
// call of the scope (see scopeRoots), as a regional root of an AWS
// account to its global root's IAM roles, and the global root to a
// regional bucket. Another scope's roots are never referred to: its
// resources are read with other credentials.
//
// Both steps go call by call: the first call, and the First calls after
// it, one at a time, as the first installs Terraform and the provider into
// the caches every root shares and its lock file pins the provider for the
// others; then the rest, options.Parallel at a time, over every scope of
// the command (see shared.callSlots). Each call records into a run of its
// own (see Run.child), merged into r in the order of calls, so the report
// doesn't depend on which finished first; with several, each logs when it
// starts and finishes listing, and generating its roots. The first call
// that fails stops the others, and its error is the scope's; nothing is
// generated after a call fails to list.
func (r *Run) importScope(ctx context.Context, options Options, calls []Call, steps callSteps) error {
	if len(calls) == 0 {
		return nil
	}
	// A command's calls share its run. Before the calls record anything: a
	// run that can't take the lock writes nothing into the output (see
	// Run.finish).
	if err := r.lockOut(options); err != nil {
		return err
	}
	if !r.used {
		// Should the calls fail before any records, the run still writes
		// its report (see Run.finish).
		r.options = options
	}
	if !options.Discover {
		// Loaded before the calls share it.
		if _, err := r.selectionFile(options); err != nil {
			return err
		}
	}
	// What the scope reads of the output is what earlier imports left.
	r.openOutput(options.Out)
	runs := make([]*Run, len(calls))
	for i := range runs {
		runs[i] = r.child()
	}
	// Whatever happens, what each call did goes into the report, in the
	// order of calls.
	defer func() {
		for _, child := range runs {
			r.merge(child)
		}
	}()
	lead := 1
	for lead < len(calls) && calls[lead].First {
		lead++
	}
	slots := r.shared.callSlots(options.Parallel)
	name := func(i int) string {
		if calls[i].Where != "" {
			return calls[i].Where
		}
		return "call " + strconv.Itoa(i+1)
	}
	// One call alone has no others to tell its steps from.
	several := len(calls) > 1
	logStep := func(i int, step string) {
		if several {
			calls[i].logStep(step)
		}
	}

	listed := make([]*listedCall, len(calls))
	err := eachCall(ctx, slots, len(calls), lead, options.Parallel, name, func(ctx context.Context, i int) error {
		calls[i].logWhere()
		var err error
		listed[i], err = steps.list(ctx, runs[i], calls[i])
		if err == nil {
			logStep(i, "listed resources")
		}
		return err
	})
	if err != nil || options.Discover {
		return err
	}
	// Before any root is generated: what goes from one to another is
	// worked out from where the roots had their resources before.
	if err := planHandovers(r.outputIndex(options.Out), listed); err != nil {
		return err
	}
	if err := markMerging(options, listed); err != nil {
		return err
	}

	byDirs := make([]map[string][]rootfile.Import, len(calls))
	for i, c := range listed {
		if c != nil {
			byDirs[i] = c.byDir
		}
	}
	// The first lock file seeds the calls after it, as one run's calls
	// share it (see generateDir). Only the calls that go one at a time set
	// it, before the others start.
	seed := r.lock
	// Once every root is generated, or the calls stopped: what a root that
	// failed left half handed over.
	defer warnIncomplete(r, listed)
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
			logStep(i, "generating the roots")
		}
		err := steps.generate(ctx, listed[i], scopeRoots(byDirs, i))
		if i < lead && seed == nil {
			seed = runs[i].lock
		}
		if err == nil && roots {
			logStep(i, "generated the roots")
		}
		return err
	})
}

// eachCall runs do for 0 to n-1: the first lead of them one at a time, in
// order, then the others, up to parallel at once (one below 1), each
// holding one of slots while it runs. The first that fails, or panics,
// cancels the context the others run with, and none start after it; its
// error is returned, with its name when there are several. An interrupt
// of ctx shows in the error of one of several.
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
		err := importSafely(name(i), func() error { return do(callCtx, i) })
		switch {
		case err == nil:
		case n > 1:
			fail(fmt.Errorf("%s: %w", name(i), err))
		default:
			fail(err)
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

	if err := ctx.Err(); n > 1 && first != nil && err != nil && !errors.Is(first, err) {
		// What failed because of the interrupt still reads as one.
		return fmt.Errorf("%w: %w", err, first)
	}
	return first
}
