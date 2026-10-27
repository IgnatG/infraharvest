// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// importWithEngine lists resources with the provider's listers, then lets
// Terraform or OpenTofu generate their configuration from import blocks,
// into run, the command's, which finishes it. No state is written.
func importWithEngine(ctx context.Context, run *Run, provider harvest.Provider, options Options, args []string) error {
	if err := checkImportOptions(options); err != nil {
		return err
	}
	if err := checkSelectionOptions(options); err != nil {
		return err
	}
	// Commands share one run across their Import calls (see Run.Do),
	// and an account imported alongside others has its own (see
	// Run.ImportEach). Before the call records anything: a run that can't
	// take the lock writes nothing into the output (see Run.finish).
	if err := run.lockOut(options); err != nil {
		return err
	}
	return importInto(ctx, run, provider, options, args)
}

// importInto imports into run. It returns an error only if the import
// couldn't run; what couldn't be imported goes into run.
func importInto(ctx context.Context, run *Run, provider harvest.Provider, options Options, args []string) error {
	c, err := listCall(ctx, run, provider, options, args)
	if err != nil || c == nil {
		return err
	}
	if err := planHandovers(options.Out, []*listedCall{c}); err != nil {
		return err
	}
	if err := markMerging(options, []*listedCall{c}); err != nil {
		return err
	}
	err = c.generate(ctx, c.byDir)
	warnIncomplete(run, []*listedCall{c})
	return err
}

// generate generates the call's roots into its run, each referring to the
// others of roots, the roots of every call of the scope by directory (see
// otherRoots): it decides what to do with each root (see decideRoot),
// then does it (see applyRoot). It returns an error only if the import
// couldn't run; what couldn't be imported goes into the run.
func (c *listedCall) generate(ctx context.Context, roots map[string][]engine.Import) error {
	run := c.run
	// What the call records is counted in its own scope, and what its
	// roots have is looked for in what it listed.
	run.scope, run.options, run.here = c.scope, c.options, c.here
	dirs := make([]string, 0, len(c.byDir))
	for dir := range c.byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	dirs = c.handovers.order(dirs)
	others := newOtherRoots(c.opts, roots, c.options.Out)
	refine, refineKey := c.stackIntent()
	for _, dir := range dirs {
		dirOpts := others.of(dir)
		// A root its stack's merge is to take, with its other
		// environments', takes no stack template: each environment's stack
		// has its own parameter values, which the merged root's
		// per-environment values don't carry yet, so the variables'
		// defaults would be one environment's.
		merged := c.merging[dir]
		if !merged {
			dirOpts.Refine, dirOpts.RefineKey = refine, refineKey
		}
		fp, err := fingerprint(c.toolchain.root.engineVersion, c.byDir[dir], dirOpts)
		if err != nil {
			return err
		}
		d, err := c.decideRoot(dir, fp)
		if err != nil {
			return err
		}
		if err := c.applyRoot(ctx, dir, fp, d, dirOpts, roots, merged && refine != nil); err != nil {
			return err
		}
	}
	run.report.LockProvider(run.lock)
	return nil
}

// applyRoot does what d decided for the root in dir (see decideRoot),
// from fingerprint fp, with dirOpts, and records it into the call's run;
// untemplated says the root's stack template is left out as its stack's
// merge is to take it. It returns an error only if the import can't go
// on.
func (c *listedCall) applyRoot(ctx context.Context, dir, fp string, d rootDecision, dirOpts engine.Options, roots map[string][]engine.Import, untemplated bool) error {
	run, out := c.run, c.options.Out
	if d.restore {
		if err := copyRoot(premergeDir(out, dir), dir); err != nil {
			return err
		}
	}
	var result *engine.Result
	var err error
	switch d.mode {
	case rootRefused:
		err = d.reason
	case rootResumed:
		log.Printf("%s: %s is unchanged since it was generated (--resume)", c.provider.Name(), dir)
		if run.lock == nil {
			run.lock, _ = os.ReadFile(filepath.Join(rootSource(out, dir), tfrun.LockFileName))
		}
		result = d.result
	case rootIncremental:
		return c.addTo(ctx, dir, fp, d, dirOpts)
	case rootFresh, rootRegenerated:
		var fatal error
		if result, err, fatal = c.generateRoot(ctx, dir, fp, d, dirOpts, roots, untemplated); fatal != nil {
			return fatal
		}
	}
	c.handovers.record(dir, result, err)
	run.addDirectory(dir, c.byDir[dir], result, err)
	if err == nil {
		c.recordMember(dir, fp, result, dirOpts)
	}
	if err == nil && run.backend != nil {
		// After the checks, which run on a local working directory: the
		// import never needs access to the state backend.
		if err := os.WriteFile(filepath.Join(rootSource(out, dir), BackendFileName), run.backend.File(relativePath(out, dir)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// terraformRun returns how the call runs the engine binary, with the
// provider's credentials.
func (c *listedCall) terraformRun(ctx context.Context) (terraformRun, error) {
	tr := terraformRun{execPath: c.toolchain.execPath, pluginCacheDir: filepath.Join(c.toolchain.cacheDir, "plugins")}
	var err error
	if tr.env, err = terraformEnv(ctx, c.provider); err != nil {
		return tr, err
	}
	tr.freshEnv = freshTerraformEnv(c.provider)
	return tr, nil
}

// addTo adds what is new to the root in dir, from fingerprint fp (see
// addToRoot), recording it into the call's run, and, for a root its stack
// merged before, sets it aside again (see setAside) and records it for
// the merge phase (see recordAdded). It returns an error only if the
// import can't go on.
func (c *listedCall) addTo(ctx context.Context, dir, fp string, d rootDecision, dirOpts engine.Options) error {
	run, out := c.run, c.options.Out
	tr, err := c.terraformRun(ctx)
	if err != nil {
		return err
	}
	added, addedResult, holds, err := run.addToRoot(ctx, dir, c.byDir[dir], dirOpts, tr)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil && holds != nil {
		if err := saveCheckpoint(out, dir, fp, holds); err != nil {
			return err
		}
	}
	if d.premerged {
		if setErr := setAside(out, dir); setErr != nil {
			return setErr
		}
		if err == nil {
			c.recordAdded(dir, fp, holds, dirOpts, addedResult)
		}
	}
	if err == nil && addedResult == nil {
		return nil // nothing new
	}
	run.addDirectory(dir, added, addedResult, err)
	return nil
}

// generateRoot generates the root in dir from scratch, from fingerprint
// fp, afresh or again (see claimRoot), with dirOpts, and returns the
// result, or why the root failed; fatal is set only if the import can't
// go on. A root that hands every resource it had over to other roots
// keeps only the removed blocks that do (see engine.Vacate).
func (c *listedCall) generateRoot(ctx context.Context, dir, fp string, d rootDecision, dirOpts engine.Options, roots map[string][]engine.Import, untemplated bool) (result *engine.Result, err, fatal error) {
	run, options, imports := c.run, c.options, c.byDir[dir]
	tr, err := c.terraformRun(ctx)
	if err != nil {
		return nil, nil, err
	}
	claimed, err := claimRoot(options.Out, dir, fp, d, run.shared)
	if err != nil {
		return nil, err, nil
	}
	claimed.removed = append(claimed.removed, c.handovers.removalsOf(dir)...)
	if len(imports) == 0 {
		log.Printf("%s: %s hands every resource it had over to other roots", c.provider.Name(), dir)
	} else {
		log.Printf("%s: generating configuration for %d resources in %s", c.provider.Name(), len(imports), dir)
	}
	// A root of another call may pin another provider release line
	// (see rootConfig): its lock file seeds this root only if it
	// meets this root's constraint.
	lock := &run.lock
	var own []byte
	if !lockMeets(run.lock, qualifiedSource(c.toolchain.binary.Registry, c.toolchain.root.provider.Source), c.toolchain.root.provider.Version) {
		lock = &own
	}
	// What the root had keeps its addresses, or moves (see
	// engine.Options.Previous); the fingerprint doesn't count it.
	dirOpts.Previous = claimed.previousRoot()
	if d.premerged {
		// Its environment applies the merged root, not this one: no
		// moved block follows what it had.
		dirOpts.Previous = nil
	}
	dirOpts.Arrivals = c.handovers.arrivalsOf(dir)
	if len(imports) == 0 {
		result, err = run.tools.vacate(dir, dirOpts)
	} else {
		result, err = run.tools.generateDir(ctx, dir, tr, imports, dirOpts, lock)
		if err == nil && untemplated {
			result.Warnings = append(result.Warnings, mergedRootNoTemplate)
		}
	}
	if err != nil && ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if err != nil {
		// A root an earlier import generated, which may be in use,
		// keeps the files it had, and its checkpoint, rather than
		// none: a later run generates it again.
		err = errors.Join(err, claimed.putBack(dir))
	}
	if err == nil && dirOpts.Previous != nil {
		err = claimed.refuseReaddressed(dir, result, imports, inOtherRoots(options.Out, roots, dir))
	}
	if err == nil {
		if err := saveCheckpoint(options.Out, dir, fp, result); err != nil {
			return nil, nil, err
		}
	}
	return result, err, nil
}

// addDirectory records what Generate did in dir, or err if it failed.
func (r *Run) addDirectory(dir string, imports []engine.Import, result *engine.Result, err error) {
	path := relativePath(r.options.Out, dir)
	if err != nil {
		r.failures = append(r.failures, fmt.Errorf("%s: %w", dir, err))
		for _, imp := range imports {
			r.failed[imp.Type]++
		}
		r.report.AddFailedDirectory(path, r.scope, err)
		return
	}
	r.report.AddDirectory(path, r.scope, result)
	for _, rej := range result.Rejected {
		r.failures = append(r.failures, fmt.Errorf("%s: %s left out (see %s): %s", dir, rej.Address, engine.RejectedFileName, strings.Join(rej.Errors, "; ")))
	}
	for _, o := range result.Owned {
		r.report.Exclude(r.scope, engine.AddressType(o.Address), o.ID, ownedByTags(o.Owner))
	}
	for _, w := range result.Warnings {
		log.Printf("%s: %s", dir, w)
	}
	for _, c := range result.Gate {
		if !c.Passed {
			r.failures = append(r.failures, fmt.Errorf("%s: %s failed: %s", dir, c.Name, strings.Join(c.Details, "; ")))
		}
	}
	if len(result.Secrets) > 0 {
		log.Printf("%s: set %d secret variables before planning (see %s)", dir, len(result.Secrets), engine.VariablesFileName)
	}
	if len(result.Moves) > 0 {
		log.Printf("%s: %d moved blocks move resources to their new addresses (see %s)", dir, len(result.Moves), engine.MovesFileName)
	}
	if len(result.Removed) > 0 {
		log.Printf("%s: %d removed blocks hand resources over to other roots: apply this root before them (see %s)", dir, len(result.Removed), engine.MovesFileName)
	}
	if len(result.Retyped) > 0 {
		log.Printf("%s: %d resources are of another name of their type now: removed blocks forget them where they were, and import blocks import them again (see %s)", dir, len(result.Retyped), engine.MovesFileName)
	}
	if len(result.Arrived) > 0 {
		log.Printf("%s: imports %d resources other roots hand over: apply those roots first (see its README)", dir, len(result.Arrived))
	}
}

// discoveredByType counts the listed resources by type.
func discoveredByType(resourcesByService map[string][]harvest.Resource) map[string]int {
	discovered := map[string]int{}
	for _, resources := range resourcesByService {
		for _, r := range resources {
			discovered[r.Address.Type]++
		}
	}
	return discovered
}
