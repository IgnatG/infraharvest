// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"

	goversion "github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/ratelimit"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/version"
)

// Import engines selectable with --engine.
const (
	EngineTerraform = "terraform"
	EngineTofu      = "tofu"
)

// Values of --format: text prints a summary, json the import report (see
// package report) on stdout too.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// Values of --modules: which modules hold clusters of resources.
const (
	// ModulesRegistry tries curated public modules, then generated local
	// modules.
	ModulesRegistry = "registry"
	// ModulesLatestUntested is ModulesRegistry with each module's newest
	// release, rather than the one its adapter is tested with.
	ModulesLatestUntested = "latest-untested"
	// ModulesLocal uses generated local modules only, such as where the
	// registry can't be reached.
	ModulesLocal = "local"
	// ModulesNone keeps every resource in the root.
	ModulesNone = "none"
)

// modulesFromRegistry reports whether --modules mode tries curated public
// modules, and the organisation's own, before generated local ones.
func modulesFromRegistry(mode string) bool {
	return mode == "" || mode == ModulesRegistry || mode == ModulesLatestUntested
}

// moduleOptions returns the engine's module options for --modules mode:
// the adapters of the modules resolved for it, and the directory, under
// out, of the generated local modules. ModulesLocal generates local modules
// only; ModulesNone keeps every resource in the root, with neither.
func moduleOptions(mode, out string, resolved []adapters.Adapter) (moduleAdapters []adapters.Adapter, modulesDir string) {
	switch {
	case modulesFromRegistry(mode):
		return resolved, filepath.Join(out, engine.ModulesDirName)
	case mode == ModulesLocal:
		return nil, filepath.Join(out, engine.ModulesDirName)
	}
	return nil, ""
}

// engineBinary is the binary --engine runs.
func engineBinary(name string) engine.Binary {
	if name == EngineTofu {
		return engine.TofuBinary
	}
	return engine.TerraformBinary
}

// importWithEngine lists resources with the provider's listers, then lets
// Terraform or OpenTofu generate their configuration from import blocks.
// No state is written.
func importWithEngine(ctx context.Context, provider harvest.Provider, options Options, args []string) error {
	if err := checkImportOptions(options); err != nil {
		return err
	}
	if err := checkSelectionOptions(options); err != nil {
		return err
	}
	// Commands share one run across their Import calls (see Run.Do),
	// and an account imported alongside others has its own (see
	// ImportEach); a call on its own finishes its own.
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
		return run.finish(Interrupted(ctx, importInto(ctx, run, provider, options, args)))
	}
	// Before the call records anything: a run that can't take the lock
	// writes nothing into the output (see Run.finish).
	if err := run.lockOut(options); err != nil {
		return err
	}
	return importInto(ctx, run, provider, options, args)
}

// InterruptContext returns ctx, cancelled on the first interrupt or
// SIGTERM, for a whole command: every Import call it makes stops, and the
// run still writes its report. The cause of the cancellation is an
// *Interrupt naming the signal, which Interrupted turns into the exit
// code. After that first signal the handler is removed, so a second one
// stops the process at once, as when a lister or a Terraform child
// doesn't stop. stop releases the handler.
func InterruptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	return interruptOn(ctx, signals, func() { signal.Stop(signals) })
}

// interruptOn returns ctx, cancelled with an *Interrupt by the first
// signal on signals, and the function that releases it; release stops the
// signals, after the first or when the context ends.
func interruptOn(ctx context.Context, signals <-chan os.Signal, release func()) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	var once sync.Once
	stop := func() {
		once.Do(release)
		cancel(context.Canceled)
	}
	go func() {
		select {
		case sig := <-signals:
			cancel(&Interrupt{Signal: sig})
		case <-ctx.Done():
		}
		stop()
	}()
	return ctx, stop
}

// Interrupt is the cause of a command's context cancelled by a signal
// (see InterruptContext).
type Interrupt struct {
	Signal os.Signal
}

func (i *Interrupt) Error() string {
	return "stopped: " + i.Signal.String()
}

// ExitCode returns the exit code of a run the signal stopped:
// report.ExitTerminated for SIGTERM, report.ExitInterrupted for an
// interrupt.
func (i *Interrupt) ExitCode() int {
	if i.Signal == syscall.SIGTERM {
		return report.ExitTerminated
	}
	return report.ExitInterrupted
}

// Interrupted returns err, the error a command run under ctx ends on,
// with the exit code of the signal that stopped it, if one cancelled ctx
// (see InterruptContext). An interrupt shows as the context's
// cancellation, or as what failed because of it, so any error counts.
func Interrupted(ctx context.Context, err error) error {
	var interrupt *Interrupt
	if err == nil || !errors.As(context.Cause(ctx), &interrupt) {
		return err
	}
	return &ExitError{Code: interrupt.ExitCode(), Err: fmt.Errorf("%w: %w", interrupt, err)}
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
	err = c.generate(ctx, c.byDir)
	warnIncomplete(run, []*listedCall{c})
	return err
}

// listedCall is an Import call whose resources are listed, selected and
// grouped into roots, ready to generate (see listCall): the calls of one
// scope are all listed before any generates, so that each root refers to
// the roots of the scope's other calls (see ImportCalls).
type listedCall struct {
	run      *Run
	provider harvest.Provider
	options  Options
	scope    string
	// here is what the call's listing found in its scope, if it can tell
	// what is gone (see listedInScope); only for --incremental.
	here *listedHere
	// byDir are the imports of each root the call generates.
	byDir map[string][]engine.Import
	// places are where each root of one of the configuration file's
	// environments goes when its stack merges (see mergePlaces), and
	// region is the scope's region.
	places map[string]memberPlace
	region string
	// handovers are what the call's roots hand over to the other roots of
	// the run, with --regenerate (see planHandovers).
	handovers *handovers
	// opts are the engine's options, without the other roots.
	opts               engine.Options
	root               *rootFiles
	binary             engine.Binary
	execPath, cacheDir string
}

// listCall lists, selects and groups into roots what one Import call
// imports into run (see importInto), recording what it found into run.
// It returns nil, with no error, for discover, which generates nothing.
func listCall(ctx context.Context, run *Run, provider harvest.Provider, options Options, args []string) (*listedCall, error) {
	if !run.used {
		// Should the import fail before it gets as far as the report, the
		// run still writes one there, with the failure (see Run.finish).
		run.options = options
	}
	binary := engineBinary(options.Engine)

	// Before the provider signs in: its clients take the rate.
	ratelimit.SetDefault(options.RateLimit)
	if err := provider.Init(ctx, args); err != nil {
		return nil, err
	}
	// The catalogue comes before the services: an overlay can add some,
	// such as an AWS cloud_control entry of a new service.
	cat, err := providerCatalogue(provider, options.Catalogue)
	if err != nil {
		return nil, err
	}
	if p, ok := provider.(harvest.ProviderWithCatalogue); ok {
		p.SetCatalogue(cat)
	}
	options.cat = cat
	if !options.Discover {
		// Discover lists every type, so that the selection file can include
		// any; an import needn't list what it leaves out anyway.
		chosen, err := run.selectionFile(options)
		if err != nil {
			return nil, err
		}
		options.unwanted = unwantedTypes(cat, chosen)
	}
	options = ResolveServices(provider, options)
	scope := discoveryScope(ctx, provider)
	run.scope = scope
	importID := importIDFunc(cat)
	listed, failures, err := listResources(ctx, provider, options, args, scope)
	if err != nil {
		return nil, err
	}
	// What failed to list, before what failed after it.
	listFailures := failures
	defaults, err := excludedByDefault(ctx, provider, cat, listed)
	if err != nil {
		failures = append(failures, fmt.Errorf("default selection: %w", err))
	}
	if defaults, err = excludeManaged(ctx, run, options, listed.resources, defaults, importID); err != nil {
		return nil, err
	}
	if !options.IncludeOwned {
		defaults = excludeOwned(listed, defaults)
	}
	if options.Discover {
		run.used = true
		run.options = options
		run.failures = append(run.failures, failures...)
		run.addDiscovered(listed, defaults, scope, importID)
		run.cover(scope, options)
		return nil, nil
	}
	chosen, err := run.selectionFile(options)
	if err != nil {
		return nil, err
	}
	selected, leftOut := run.selectResources(listed, defaults, chosen, scope, importID)
	// The configuration file's environments of the scope, if any; none
	// keeps the layout as without them.
	envNames := run.environments.For(providerDir(provider), scopeAccount(scope))
	pattern := withEnvLayout(options.Layout, len(options.EnvTags) > 0 || len(envNames) > 0)
	// Where the roots would be without the file's environments, for those
	// an earlier import generated there, which stay (see keptRoots).
	layouts, err := rootLayouts(ctx, provider, pattern, withEnvLayout(options.Layout, len(options.EnvTags) > 0))
	if err != nil {
		return nil, err
	}
	options.Layout = layouts[0]
	unsplitLayout := layouts[1]

	cacheDir, err := infraharvestCacheDir()
	if err != nil {
		return nil, err
	}
	execPath, err := options.tools.find(ctx, binary, options.TerraformPath, filepath.Join(cacheDir, binary.Name))
	if err != nil {
		return nil, err
	}
	var moduleAdapters []adapters.Adapter
	var newerModules []report.ModuleVersion
	var constraints []moduleConstraint
	if modulesFromRegistry(options.Modules) {
		if options.Modules == ModulesLatestUntested {
			log.Printf("warning: --modules %s calls module releases no adapter is tested with: terraform init downloads them, and plan runs their code with the credentials this import uses; review what they call before you rely on the output", ModulesLatestUntested)
		}
		moduleAdapters, newerModules, constraints = resolveAdapters(ctx, http.DefaultClient, "", applicableAdapters(adapters.For(provider.Name()), selected), engineProvider(provider).Source, options.Modules == ModulesLatestUntested)
		// The organisation's own modules come first.
		own := applicableAdapters(catalogueAdapters(run.shared.ownAdapters(ctx, execPath, filepath.Join(cacheDir, "modules"), provider.Name(), run.modules), provider.Name()), selected)
		moduleAdapters = append(own, moduleAdapters...)
	}
	root, err := options.tools.root(ctx, binary.Registry, execPath, engineProvider(provider), constraints)
	if err != nil {
		return nil, err
	}
	moduleAdapters = withoutModules(moduleAdapters, root.refused)
	var unusedModules []report.UnusedModule
	for _, c := range root.refused {
		unusedModules = append(unusedModules, report.UnusedModule{Source: c.module, Version: c.version, Accepts: c.constraint.String()})
	}
	if err := writeGitignore(options.Out); err != nil {
		return nil, err
	}

	// Children follow their parent: only selected resources have them.
	resourcesByService, parents, childFailures := withChildImports(ctx, provider, selected, childParallel(provider, options))
	failures = append(failures, childFailures...)
	var here *listedHere
	if options.Incremental {
		var why string
		if here, why = listedInScope(provider, cat, options, pattern, scope, listed, listFailures, resourcesByService, importID); here == nil {
			log.Printf("%s: the roots' resources this run doesn't find aren't checked for whether they are still in the cloud: %s (--incremental)", provider.Name(), why)
		}
	}
	opts := engineOptions(provider, cat, root)
	opts.IncludeOwned = options.IncludeOwned
	opts.CommandTimeout = options.EngineTimeout
	opts.KeepOwned = keptOwned(selected, listed, importID)
	opts.External = leftOut
	opts.Adapters, opts.ModulesDir = moduleOptions(options.Modules, options.Out, moduleAdapters)
	unplaced := func(harvest.Resource) string { return "" }
	byEnvTags := func(layout string) func(harvest.Resource) string {
		if !strings.Contains(layout, "{env}") {
			return unplaced
		}
		keys := options.EnvTags
		if len(keys) == 0 {
			keys = defaultEnvTags
		}
		envs := environments(resourcesByService, listed.tags, byTagKeys(keys), parents, importID)
		return func(r harvest.Resource) string { return envs[tagKey(r)] }
	}
	envOf := byEnvTags(options.Layout)
	if len(envNames) > 0 {
		envOf = configuredEnvOf(run.environments, envNames, resourcesByService, listed.tags, parents, importID)
	}
	dirOf := layoutDirs(options.Layout, options.Out, providerDir(provider), envOf)
	var kept *keptRoots
	if len(envNames) > 0 {
		kept = newKeptRoots(options.Out)
		dirOf = kept.dirs(dirOf, layoutDirs(unsplitLayout, options.Out, providerDir(provider), byEnvTags(unsplitLayout)), envOf)
	}
	nameOf := resourceNames(provider.Name(), listed, resourcesByService)
	fileOf := func(service string, r harvest.Resource) string {
		return configFile(provider.Name(), service, r.Address.Type, cat)
	}
	byDir, skipped := importsByDir(provider.Name(), resourcesByService, importID, dirOf, nameOf, fileOf)
	withResourceOmissions(byDir, resourcesByService, cat, importID)
	var places map[string]memberPlace
	if options.MergeEnvironments && len(envNames) > 0 {
		stackLayouts, err := rootLayouts(ctx, provider, stackLayout(pattern))
		if err != nil {
			return nil, err
		}
		places = mergePlaces(resourcesByService, envOf, dirOf, layoutDirs(stackLayouts[0], options.Out, providerDir(provider), envOf), kept, byDir)
	}

	run.used = true
	run.options = options
	run.report.TagKeys = options.ReportTags
	if kept != nil {
		run.report.KeptRoots = append(run.report.KeptRoots, kept.report(scope)...)
	}
	// Each call adds what it selected: another region's may hold the
	// provider back, or find other modules.
	run.report.Add(report.Manifest{
		Tool:     report.Component{Name: "infraharvest", Version: version.Version},
		Engine:   report.Component{Name: binary.Name, Version: root.engineVersion},
		Provider: report.Provider{Source: qualifiedSource(binary.Registry, root.provider.Source), Constraint: root.provider.Version, HeldBack: root.heldBack},
		Modules:  newerModules,
		Unused:   unusedModules,
	})
	for _, f := range failures {
		run.report.Failures = append(run.report.Failures, f.Error())
	}
	run.failures = append(run.failures, failures...)
	for typ, n := range skipped {
		run.skipped[typ] += n
	}
	// What the selection left out, and what it keeps with their children.
	counted := len(leftOut)
	for typ, n := range discoveredByType(resourcesByService) {
		run.discovered[typ] += n
		counted += n
	}
	if scope != "" {
		run.report.AddDiscovered(scope, counted)
	}

	if options.Incremental {
		// What an earlier import put in another root stays there.
		held, err := heldElsewhere(options.Out, byDir)
		if err != nil {
			return nil, err
		}
		run.leaveOutHeld(byDir, held)
	}
	return &listedCall{run: run, provider: provider, options: options, scope: scope, here: here, byDir: byDir, places: places, region: scopeRegion(scope), opts: opts, root: root, binary: binary, execPath: execPath, cacheDir: cacheDir}, nil
}

// generate generates the call's roots into its run, each referring to the
// others of roots, the roots of every call of the scope by directory (see
// otherRoots). It returns an error only if the import couldn't run;
// what couldn't be imported goes into the run.
func (c *listedCall) generate(ctx context.Context, roots map[string][]engine.Import) error {
	run, provider, options, byDir, opts, root, binary := c.run, c.provider, c.options, c.byDir, c.opts, c.root, c.binary
	execPath, cacheDir := c.execPath, c.cacheDir
	// What the call records is counted in its own scope, and what its
	// roots have is looked for in what it listed.
	run.scope, run.options, run.here = c.scope, options, c.here
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	dirs = c.handovers.order(dirs)
	others := newOtherRoots(opts, roots, options.Out)
	for _, dir := range dirs {
		if err := c.handovers.refusal(dir); err != nil {
			c.handovers.record(dir, nil, err)
			run.addDirectory(dir, byDir[dir], nil, err)
			continue
		}
		dirOpts := others.of(dir)
		fp, err := fingerprint(root.engineVersion, byDir[dir], dirOpts)
		if err != nil {
			return err
		}
		var result *engine.Result
		if options.Resume {
			if result, err = resumed(options.Out, dir, fp); err != nil {
				return err
			}
		}
		tr := terraformRun{execPath: execPath, pluginCacheDir: filepath.Join(cacheDir, "plugins")}
		if result == nil {
			if tr.env, err = terraformEnv(ctx, provider); err != nil {
				return err
			}
			tr.freshEnv = freshTerraformEnv(provider)
		}
		// A root its stack's merge set aside (see premergeDir).
		isPremerged, err := premerged(options.Out, dir)
		if err != nil {
			return err
		}
		incremental := false
		if result == nil && options.Incremental {
			if isPremerged {
				// Added to as it was, then set aside again, for the merge
				// phase to merge its stack again (see MergeEnvironments).
				if err := copyRoot(premergeDir(options.Out, dir), dir); err != nil {
					return err
				}
			}
			if incremental, err = engine.HasConfiguration(dir); err != nil {
				return err
			}
			if incremental && options.Resume {
				// With --resume, a root whose import didn't finish is
				// generated again rather than added to (see addToRoot).
				unfinished, err := unfinishedRoot(options.Out, dir)
				if err != nil {
					return err
				}
				incremental = !unfinished
			}
		}
		switch {
		case result != nil:
			log.Printf("%s: %s is unchanged since it was generated (--resume)", provider.Name(), dir)
			if run.lock == nil {
				run.lock, _ = os.ReadFile(filepath.Join(rootSource(options.Out, dir), engine.LockFileName))
			}
		case incremental:
			added, addedResult, holds, err := run.addToRoot(ctx, dir, byDir[dir], dirOpts, tr)
			if err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil && holds != nil {
				if err := saveCheckpoint(options.Out, dir, fp, holds); err != nil {
					return err
				}
			}
			if isPremerged {
				if setErr := setAside(options.Out, dir); setErr != nil {
					return setErr
				}
				if err == nil {
					c.recordAdded(dir, fp, holds, dirOpts, addedResult)
				}
			}
			if err == nil && addedResult == nil {
				continue // nothing new
			}
			run.addDirectory(dir, added, addedResult, err)
			continue
		default:
			// A root an earlier import generated is generated again, from
			// scratch, with --regenerate, or with --resume if that import
			// didn't finish; a directory no import wrote is left alone. With
			// --incremental, a root gets here only if it has no resources or,
			// with --resume, its import didn't finish.
			// A root that would hand over what a root generated before it
			// didn't import is left as it was, still holding it.
			if err = c.handovers.refuseUnreceived(dir); err != nil {
				c.handovers.record(dir, nil, err)
				run.addDirectory(dir, byDir[dir], nil, err)
				continue
			}
			if isPremerged && !options.Regenerate && !options.Resume {
				err = fmt.Errorf("%s was merged into the root its stack's environments share, which may be in use: pass --regenerate to generate both again, or --incremental to add what is new", dir)
				c.handovers.record(dir, nil, err)
				run.addDirectory(dir, byDir[dir], nil, err)
				continue
			}
			var claimed *claim
			if claimed, err = claimRoot(options.Out, dir, fp, options.Resume, options.Regenerate || options.Incremental); err != nil {
				c.handovers.record(dir, nil, err)
				run.addDirectory(dir, byDir[dir], nil, err)
				continue
			}
			claimed.removed = append(claimed.removed, c.handovers.removalsOf(dir)...)
			if len(byDir[dir]) == 0 {
				log.Printf("%s: %s hands every resource it had over to other roots", provider.Name(), dir)
			} else {
				log.Printf("%s: generating configuration for %d resources in %s", provider.Name(), len(byDir[dir]), dir)
			}
			// A root of another call may pin another provider release line
			// (see rootConfig): its lock file seeds this root only if it
			// meets this root's constraint.
			lock := &run.lock
			var own []byte
			if !lockMeets(run.lock, qualifiedSource(binary.Registry, root.provider.Source), root.provider.Version) {
				lock = &own
			}
			// What the root had keeps its addresses, or moves (see
			// engine.Options.Previous); the fingerprint doesn't count it.
			dirOpts.Previous = claimed.previousRoot()
			if isPremerged {
				// Its environment applies the merged root, not this one: no
				// moved block follows what it had.
				dirOpts.Previous = nil
			}
			dirOpts.Arrivals = c.handovers.arrivalsOf(dir)
			if len(byDir[dir]) == 0 {
				result, err = engine.Vacate(dir, dirOpts)
			} else {
				result, err = options.tools.generateDir(ctx, dir, tr, byDir[dir], dirOpts, lock)
			}
			if err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil && dirOpts.Previous != nil {
				err = claimed.refuseReaddressed(dir, result, byDir[dir], inOtherRoots(options.Out, roots, dir))
			}
			if err == nil {
				if err := saveCheckpoint(options.Out, dir, fp, result); err != nil {
					return err
				}
			}
		}
		c.handovers.record(dir, result, err)
		run.addDirectory(dir, byDir[dir], result, err)
		if err == nil {
			c.recordMember(dir, fp, result, dirOpts)
		}
		if err == nil && run.backend != nil {
			// After the checks, which run on a local working directory: the
			// import never needs access to the state backend.
			if err := os.WriteFile(filepath.Join(rootSource(options.Out, dir), BackendFileName), run.backend.File(relativePath(options.Out, dir)), 0o644); err != nil {
				return err
			}
		}
	}
	run.report.Provider.Version = engine.LockedVersion(run.lock, run.report.Provider.Source)
	return nil
}

// inOtherRoots returns, by type and import ID, the root of roots other
// than dir that each of their resources goes in, by its path relative to
// out.
func inOtherRoots(out string, roots map[string][]engine.Import, dir string) map[engine.External]string {
	other := map[engine.External]string{}
	for d, imports := range roots {
		if d == dir {
			continue
		}
		for _, imp := range imports {
			other[engine.External{Type: imp.Type, ID: imp.ID}] = relativePath(out, d)
		}
	}
	return other
}

// engineOptions configures engine.Generate for provider's directories,
// with what cat knows about its types.
func engineOptions(provider harvest.Provider, cat *catalogue.Catalogue, root *rootFiles) engine.Options {
	opts := engine.Options{Config: root.files, Scanners: engine.DefaultScanners(), Omit: cat.OmitFor(root.version), Defaults: cat.DefaultsFor(root.version), StateOnly: cat.StateOnlyFor(root.version), KeepEmpty: cat.KeepEmpty(), SplitFiles: true, TidyLabels: true}
	if sources := cat.DataSources(); len(sources) > 0 {
		opts.DataSources = map[string]engine.DataSource{}
		for typ, d := range sources {
			opts.DataSources[typ] = engine.DataSource{Type: d.Type, Argument: d.Argument}
		}
	}
	for _, typ := range cat.Types() {
		if e, ok := cat.Entry(typ); ok && e.ARN != nil {
			if opts.ARNs == nil {
				opts.ARNs = map[string]engine.ARN{}
			}
			opts.ARNs[typ] = engine.ARN{Service: e.ARN.Service, ResourceType: e.ARN.ResourceType}
		}
	}
	if placeholders := cat.Placeholders(); len(placeholders) > 0 {
		opts.Placeholders = map[string]engine.Placeholder{}
		for typ, p := range placeholders {
			opts.Placeholders[typ] = engine.Placeholder{OneOf: p.OneOf, Set: p.Set, Ignore: p.Ignore, Reason: p.Reason}
		}
	}
	if lifecycles := cat.Lifecycles(); len(lifecycles) > 0 {
		opts.Lifecycles = map[string]engine.Lifecycle{}
		for typ, l := range lifecycles {
			opts.Lifecycles[typ] = engine.Lifecycle{CreateBeforeDestroy: l.CreateBeforeDestroy, Reason: l.Reason}
		}
	}
	if withDefaultTags, ok := provider.(harvest.ProviderWithDefaultTags); ok {
		attribute, block, reserved := withDefaultTags.DefaultTags()
		opts.DefaultTags = &engine.DefaultTags{Provider: provider.Name(), Attribute: attribute, Block: block, ReservedPrefix: reserved}
		if a, ok := provider.(harvest.ProviderWithDefaultTagsArgument); ok {
			opts.DefaultTags.Argument = a.DefaultTagsArgument()
		}
	}
	if withEffectiveTags, ok := provider.(harvest.ProviderWithEffectiveTags); ok {
		attribute, effective, reserved := withEffectiveTags.EffectiveTags()
		opts.EffectiveTags = &engine.EffectiveTags{Attribute: attribute, Effective: effective, ReservedPrefix: reserved}
	}
	if withInherited, ok := provider.(harvest.ProviderWithInheritedArguments); ok {
		opts.Inherited = withInherited.InheritedArguments()
	}
	if withModuleProviders, ok := provider.(harvest.ProviderWithModuleProviders); ok {
		opts.ModuleProviders = map[string]engine.Provider{}
		for name, p := range withModuleProviders.ModuleProviders() {
			if name != provider.Name() {
				opts.ModuleProviders[name] = engine.Provider{Name: name, Source: p.Source, Config: p.Config}
			}
		}
	}
	return opts
}

// addDirectory records what Generate did in dir, or err if it failed.
func (r *Run) addDirectory(dir string, imports []engine.Import, result *engine.Result, err error) {
	reported := report.Directory{Path: relativePath(r.options.Out, dir), Scope: r.scope}
	if err != nil {
		r.failures = append(r.failures, fmt.Errorf("%s: %w", dir, err))
		reported.Error = err.Error()
		for _, imp := range imports {
			r.failed[imp.Type]++
		}
		r.report.Directories = append(r.report.Directories, reported)
		return
	}
	// Each resource at its address in the root, in the module call
	// Generate moved it into if any (see engine.Result.Addresses); a
	// result without addresses, as in a checkpoint an older release
	// wrote, has only the labels Generate gave them.
	at := map[engine.External]string{}
	for _, m := range result.Addresses {
		at[engine.External{Type: m.Type, ID: m.ID}] = m.Address
	}
	reported.Imported = make([]report.Resource, 0, len(result.Imported))
	for _, imp := range result.Imported {
		address, ok := at[engine.External{Type: imp.Type, ID: imp.ID}]
		if !ok {
			address = imp.Type + "." + imp.Name
		}
		reported.Imported = append(reported.Imported, report.Resource{Address: address, ID: imp.ID})
	}
	for _, rej := range result.Rejected {
		r.failures = append(r.failures, fmt.Errorf("%s: %s left out (see %s): %s", dir, rej.Address, engine.RejectedFileName, strings.Join(rej.Errors, "; ")))
		reported.LeftOut = append(reported.LeftOut, report.LeftOut{Address: rej.Address, ID: rej.ID, Errors: rej.Errors})
	}
	for _, m := range result.Modules {
		reported.Modules = append(reported.Modules, report.Module{Name: m.Name, Source: m.Source, Version: m.Version, Resources: m.Resources, Declined: m.Declined})
	}
	for _, s := range result.Secrets {
		reported.Secrets = append(reported.Secrets, report.Secret{Variable: s.Variable, Address: s.Address, Attribute: s.Attribute})
	}
	reported.Warnings = result.Warnings
	for _, o := range result.Owned {
		r.report.Excluded = append(r.report.Excluded, report.Excluded{Type: resourceTypeOf(o.Address), ID: o.ID, Reason: ownedReason(o.Owner), Scope: r.scope})
	}
	for _, w := range result.Warnings {
		log.Printf("%s: %s", dir, w)
	}
	for _, c := range result.Gate {
		reported.Checks = append(reported.Checks, report.Check{Name: c.Name, Passed: c.Passed, Skipped: c.Skipped, Details: c.Details})
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
	if len(result.Arrived) > 0 {
		log.Printf("%s: imports %d resources other roots hand over: apply those roots first (see its README)", dir, len(result.Arrived))
	}
	r.recordMoves(reported.Path, result)
	r.report.Directories = append(r.report.Directories, reported)
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

// relativePath returns dir relative to the output directory, with forward
// slashes, so reports don't depend on where or on which OS they ran.
func relativePath(outputDir, dir string) string {
	rel, err := filepath.Rel(outputDir, dir)
	if err != nil {
		rel = dir
	}
	return filepath.ToSlash(rel)
}

// terraformRun is how a root's Terraform runs: the binary, the plugin cache
// every root shares, and what the provider adds to its environment, such
// as fresh credentials (see terraformEnv), and, when the provider adds
// any, how to get them again before each plan.
type terraformRun struct {
	execPath, pluginCacheDir string
	env                      map[string]string
	freshEnv                 func(context.Context) (map[string]string, error)
}

// in returns the Terraform runner of dir: one that asks freshEnv for its
// environment before each plan, when it is set (see
// engine.NewFreshEnvTerraform).
func (t terraformRun) in(dir string) (engine.Terraform, error) {
	if t.freshEnv != nil {
		return engine.NewFreshEnvTerraform(dir, t.execPath, t.pluginCacheDir, t.env, t.freshEnv)
	}
	tf, err := engine.NewTerraform(dir, t.execPath, t.pluginCacheDir, t.env)
	if err != nil {
		return nil, err
	}
	return tf, nil
}

// freshTerraformEnv returns how to get provider's Terraform environment
// again (see terraformEnv), for each plan, or nil when the provider adds
// nothing to it.
func freshTerraformEnv(provider harvest.Provider) func(context.Context) (map[string]string, error) {
	if _, ok := provider.(harvest.ProviderWithTerraformEnv); !ok {
		return nil
	}
	return func(ctx context.Context) (map[string]string, error) {
		return terraformEnv(ctx, provider)
	}
}

// terraformEnv returns what Terraform needs in its environment to import
// provider's resources, such as the credentials it lists with (see
// harvest.ProviderWithTerraformEnv), fresh for each root and each plan.
func terraformEnv(ctx context.Context, provider harvest.Provider) (map[string]string, error) {
	withEnv, ok := provider.(harvest.ProviderWithTerraformEnv)
	if !ok {
		return nil, nil
	}
	env, err := withEnv.TerraformEnv(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: credentials for Terraform: %w", provider.Name(), err)
	}
	return env, nil
}

// generateDir runs engine.Generate in dir, seeding it with *lock if set and
// keeping its lock file in *lock otherwise.
func generateDir(ctx context.Context, dir string, tr terraformRun, imports []engine.Import, opts engine.Options, lock *[]byte) (*engine.Result, error) {
	tf, err := tr.in(dir)
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, engine.LockFileName)
	if *lock != nil {
		if err := os.WriteFile(lockPath, *lock, 0o644); err != nil {
			return nil, err
		}
	}
	result, err := engine.Generate(ctx, tf, dir, imports, opts)
	if *lock == nil {
		if content, readErr := os.ReadFile(lockPath); readErr == nil {
			*lock = content
		}
	}
	return result, err
}

// lockMeets reports whether the provider at source that lock records meets
// constraint, so that lock can seed a root with that constraint: terraform
// init fails on a locked version the configuration doesn't accept. A lock
// that records no version of source, or a constraint or version that
// doesn't parse, counts as meeting it, as before such a check.
func lockMeets(lock []byte, source, constraint string) bool {
	locked := engine.LockedVersion(lock, source)
	if locked == "" || constraint == "" {
		return true
	}
	v, err := goversion.NewVersion(locked)
	if err != nil {
		return true
	}
	c, err := goversion.NewConstraint(constraint)
	if err != nil {
		return true
	}
	return c.Check(v)
}

// rootFiles are the files every output directory starts with, and the
// versions they pin.
type rootFiles struct {
	files         map[string][]byte // versions.tf and providers.tf
	engineVersion string
	provider      engine.Provider // Version is the constraint
	// version is the provider version the constraint pins the release
	// line of, which decides the catalogue entries that depend on it.
	version *goversion.Version
	// heldBack says which modules keep the provider below its newest
	// release, within its newest major release, if any do.
	heldBack string
	// refused are the module versions that accept only an older major
	// release of the provider: their adapters aren't used.
	refused []moduleConstraint
}

// rootConfig renders versions.tf and providers.tf for every output
// directory: required_version from the oldest Terraform release generated
// configuration works with (engine.MinimumVersion; Generate raises it for
// what a root uses), and the provider pinned to its newest minor release
// line in registry (the engine's default registry), unless its source names
// another: the newest release the module versions the import may call
// accept (constraints). A module version that accepts no release of the
// provider's newest major release line doesn't hold the provider back: it
// is refused, and the import doesn't call it (see newestMajor). A
// registryURL replaces the registry, for tests.
func rootConfig(ctx context.Context, client *http.Client, registry, registryURL, execPath string, p engine.Provider, constraints []moduleConstraint) (*rootFiles, error) {
	engineVersion, err := engine.BinaryVersion(ctx, execPath)
	if err != nil {
		return nil, err
	}
	source := qualifiedSource(registry, p.Source)
	latest, err := engine.LatestProviderVersion(ctx, client, registryURL, source)
	if err != nil {
		return nil, err
	}
	constraints, refused := newestMajor(ctx, client, registryURL, source, latest, constraints)
	for _, c := range refused {
		log.Printf("%s %s: not called: it accepts %s %s, and the newest release is %s; its resources stay in the root", c.module, c.version, p.Source, c.constraint, latest)
	}
	chosen, held := latest, ""
	if len(constraints) > 0 {
		all := make([]goversion.Constraints, len(constraints))
		for i, c := range constraints {
			all[i] = c.constraint
		}
		v, err := engine.ProviderVersion(ctx, client, registryURL, source, all)
		switch {
		case err != nil:
			// The modules' calls then fail their plans and are declined.
			log.Printf("%s: %v; pinning the newest release, %s", p.Source, err, latest)
		case v.LessThan(latest):
			chosen, held = v, heldBack(latest, constraints)
			log.Printf("%s: pinning %s: %s", p.Source, v, held)
		}
	}
	p.Version = engine.ProviderConstraint(chosen)
	providers, err := engine.ProvidersFile(p)
	if err != nil {
		return nil, err
	}
	return &rootFiles{
		files: map[string][]byte{
			engine.VersionsFileName:  engine.VersionsFile(engine.RequiredVersion(engine.MinimumVersion), p),
			engine.ProvidersFileName: providers,
		},
		engineVersion: engineVersion.String(),
		provider:      p,
		heldBack:      held,
		refused:       refused,
		version:       chosen,
	}, nil
}

// writeGitignore writes a .gitignore for Terraform into the output
// directory, unless it has one.
func writeGitignore(outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(outputDir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	// Atomically: accounts imported in parallel write it at the same time.
	return fsutil.WriteFile(path, []byte(engine.GitignoreFile), 0o644)
}

// checkImportOptions rejects values of --format and --modules the import
// would otherwise silently ignore.
func checkImportOptions(options Options) error {
	if options.Format != "" && options.Format != FormatText && options.Format != FormatJSON {
		return fmt.Errorf("--format must be %s or %s, not %q", FormatText, FormatJSON, options.Format)
	}
	switch options.Modules {
	case "", ModulesRegistry, ModulesLatestUntested, ModulesLocal, ModulesNone:
	default:
		return fmt.Errorf("--modules must be %s, %s, %s or %s, not %q", ModulesRegistry, ModulesLatestUntested, ModulesLocal, ModulesNone, options.Modules)
	}
	return checkFilters(options)
}

// checkFilters rejects an --id, --tag or --where that doesn't parse. --id
// applies to every service; a filter on a tag or another attribute only
// takes effect where the service's lister passes it to the API it lists
// with (such as AWS EC2 instance tags), so it says so.
func checkFilters(options Options) error {
	filters, err := options.filters()
	if err != nil {
		return err
	}
	for _, f := range filters {
		if f.Path != "id" {
			log.Printf("a filter on %s only applies to services whose lister supports it (see the provider's page); others list everything", f.Path)
		}
	}
	return nil
}

// layoutDirs returns the root of each resource of a service, by layout
// (see rootLayouts) under out, with dirName for {provider} and envOf for
// {env}.
func layoutDirs(layout, out, dirName string, envOf func(harvest.Resource) string) func(service string, r harvest.Resource) string {
	return func(service string, r harvest.Resource) string {
		return rootDir(layout, rootValues{out: out, provider: dirName, service: service, env: envOf(r)})
	}
}

// importsByDir groups resources into import blocks per output directory,
// the root dirOf names for each, each named by nameOf and in the file
// fileOf names. providerName is the Terraform provider the roots declare.
// It leaves out, logs and counts by type resources Terraform can't
// import.
func importsByDir(providerName string, resourcesByService map[string][]harvest.Resource, importID func(harvest.Resource) (string, bool), dirOf func(service string, r harvest.Resource) string, nameOf func(harvest.Resource) string, fileOf func(service string, r harvest.Resource) string) (map[string][]engine.Import, map[string]int) {
	byDir := map[string][]engine.Import{}
	skipped := map[string]int{}
	for service, resources := range resourcesByService {
		for _, r := range resources {
			dir := dirOf(service, r)
			id, ok := importID(r)
			if !ok {
				skipped[r.Address.Type]++
				continue
			}
			byDir[dir] = append(byDir[dir], engine.Import{
				Type:     r.Address.Type,
				Name:     nameOf(r),
				File:     fileOf(service, r),
				ID:       id,
				Provider: importProvider(providerName, r.Address.Type),
			})
		}
	}
	types := make([]string, 0, len(skipped))
	for typ := range skipped {
		types = append(types, typ)
	}
	sort.Strings(types)
	for _, typ := range types {
		log.Printf("%s: skipping %d %s: Terraform can't import this resource type", providerName, skipped[typ], typ)
	}
	return byDir, skipped
}

// withResourceOmissions sets, on each import of byDir, the arguments the
// catalogue leaves out of its resource's configuration depending on what
// its lister recorded (see catalogue.Catalogue.ResourceOmit), matching
// imports to the resources they came from by type and import ID.
func withResourceOmissions(byDir map[string][]engine.Import, resourcesByService map[string][]harvest.Resource, cat *catalogue.Catalogue, importID func(harvest.Resource) (string, bool)) {
	omit := map[string][]string{}
	for _, resources := range resourcesByService {
		for _, r := range resources {
			if r.Address == nil || r.Observed == nil {
				continue
			}
			extra := cat.ResourceOmit(r.Address.Type, r.Observed.Attributes)
			if len(extra) == 0 {
				continue
			}
			if id, ok := importID(r); ok {
				omit[r.Address.Type+"\x00"+id] = extra
			}
		}
	}
	if len(omit) == 0 {
		return
	}
	for _, imports := range byDir {
		for i, imp := range imports {
			imports[i].Omit = omit[imp.Type+"\x00"+imp.ID]
		}
	}
}

// otherRoots is what the roots of an import manage, sorted once for the
// options of each of them (see of): with a {service} or {env} layout, a
// call has tens of roots, and each would otherwise sort what all the
// others manage again.
type otherRoots struct {
	opts engine.Options
	// several is whether there is more than one root, without which there
	// is no other root.
	several bool
	// external and resources are opts' own and what every root manages,
	// each with the root's directory ("" for opts' own), in the order of
	// the options of a root.
	external  []ofRoot[engine.External]
	resources []ofRoot[engine.RootResource]
}

// ofRoot is a value, of the root in dir.
type ofRoot[T any] struct {
	value T
	dir   string
}

// newOtherRoots indexes the roots of byDir, under the output directory
// out, for the options of each, which start from opts.
func newOtherRoots(opts engine.Options, byDir map[string][]engine.Import, out string) otherRoots {
	o := otherRoots{opts: opts, several: len(byDir) >= 2}
	if !o.several {
		return o
	}
	for _, e := range opts.External {
		o.external = append(o.external, ofRoot[engine.External]{value: e})
	}
	for _, r := range opts.OtherRoots {
		o.resources = append(o.resources, ofRoot[engine.RootResource]{value: r})
	}
	for dir, imports := range byDir {
		root := relativePath(out, dir)
		for _, imp := range imports {
			// External resources are only of use to the data sources that
			// read them (see engine.Options.External): the others would
			// only make the root's fingerprint change with resources it
			// can't refer to.
			if _, ok := opts.DataSources[imp.Type]; ok {
				o.external = append(o.external, ofRoot[engine.External]{value: engine.External{Type: imp.Type, ID: imp.ID, Name: imp.Name}, dir: dir})
			}
			o.resources = append(o.resources, ofRoot[engine.RootResource]{value: engine.RootResource{Type: imp.Type, ID: imp.ID, Name: engine.Label(imp.Name), Root: root}, dir: dir})
		}
	}
	// A name before none: the data source that reads it is named by it.
	unnamed := func(e engine.External) int {
		if e.Name == "" {
			return 1
		}
		return 0
	}
	slices.SortFunc(o.external, func(x, y ofRoot[engine.External]) int {
		a, b := x.value, y.value
		return cmp.Or(strings.Compare(a.Type, b.Type), strings.Compare(a.ID, b.ID), cmp.Compare(unnamed(a), unnamed(b)), strings.Compare(a.Name, b.Name))
	})
	slices.SortFunc(o.resources, func(x, y ofRoot[engine.RootResource]) int {
		a, b := x.value, y.value
		return cmp.Or(strings.Compare(a.Type, b.Type), strings.Compare(a.ID, b.ID), strings.Compare(a.Root, b.Root), strings.Compare(a.Name, b.Name))
	})
	return o
}

// of returns the options for the root in dir, with what the other roots
// manage as external: references to it read it, as references between
// roots do, or a variable says where it comes from.
func (o otherRoots) of(dir string) engine.Options {
	if !o.several {
		return o.opts
	}
	dirOpts := o.opts
	dirOpts.External, dirOpts.OtherRoots = nil, nil
	for _, e := range o.external {
		// Each resource once, by its first entry.
		if (e.dir != "" && e.dir == dir) || (len(dirOpts.External) > 0 && sameResource(dirOpts.External[len(dirOpts.External)-1], e.value)) {
			continue
		}
		dirOpts.External = append(dirOpts.External, e.value)
	}
	for _, r := range o.resources {
		if (r.dir != "" && r.dir == dir) || (len(dirOpts.OtherRoots) > 0 && dirOpts.OtherRoots[len(dirOpts.OtherRoots)-1] == r.value) {
			continue
		}
		dirOpts.OtherRoots = append(dirOpts.OtherRoots, r.value)
	}
	return dirOpts
}

// sameResource reports whether a and b are the same resource, whatever
// they are named.
func sameResource(a, b engine.External) bool {
	return a.Type == b.Type && a.ID == b.ID
}

// importProvider returns the provider an import block must name:
// providerName, the local name the root's required_providers declares,
// when it isn't the one Terraform infers from the resource type (the part
// before the first underscore), such as google-beta for google_* resources.
// It returns "" when the two agree.
func importProvider(providerName, resourceType string) string {
	if implied, _, _ := strings.Cut(resourceType, "_"); implied == providerName {
		return ""
	}
	return providerName
}

// providerDir returns the directory provider's roots go into, the
// {provider} of --layout: the name of its command (see
// harvest.ProviderWithDirectory), else its Terraform provider's name.
func providerDir(provider harvest.Provider) string {
	if withDir, ok := provider.(harvest.ProviderWithDirectory); ok {
		return withDir.Directory()
	}
	return provider.Name()
}

// engineProvider describes the provider for the generated configuration.
func engineProvider(provider harvest.Provider) engine.Provider {
	name := provider.Name()
	source := "hashicorp/" + name
	if withSource, ok := provider.(harvest.ProviderWithRegistrySource); ok {
		source = withSource.RegistrySource()
	}
	var config map[string]interface{}
	if providers, ok := provider.ProviderBlock()["provider"].(map[string]interface{}); ok {
		config, _ = providers[name].(map[string]interface{})
	}
	return engine.Provider{Name: name, Source: source, Config: config}
}

func infraharvestCacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find a cache directory for Terraform downloads: %w", err)
	}
	return filepath.Join(dir, "infraharvest"), nil
}

// qualifiedSource adds registry to a provider source without a host:
// hashicorp/aws resolves to registry.terraform.io/hashicorp/aws in
// Terraform and registry.opentofu.org/hashicorp/aws in OpenTofu.
func qualifiedSource(registry, source string) string {
	if strings.Count(source, "/") == 1 {
		return registry + "/" + source
	}
	return source
}

// withChildImports adds to each service's resources the child resources
// the provider lists for them (see harvest.ProviderWithChildImports),
// and returns each child's parent, by tagKey, and the resources it
// couldn't list children for as failures. Each resource takes calls of its
// own, such as an S3 bucket's configuration probes, so up to parallel
// resources are asked at once (one below 1); the children and the
// failures keep the order of the services, by name, and of their
// resources.
func withChildImports(ctx context.Context, provider harvest.Provider, resourcesByService map[string][]harvest.Resource, parallel int) (map[string][]harvest.Resource, map[string]string, []error) {
	parents := map[string]string{}
	withChildren, ok := provider.(harvest.ProviderWithChildImports)
	if !ok {
		return resourcesByService, parents, nil
	}
	type ask struct {
		service  string
		r        harvest.Resource
		children []harvest.Resource
		err      error
	}
	var asks []ask
	for _, service := range slices.Sorted(maps.Keys(resourcesByService)) {
		for _, r := range resourcesByService[service] {
			asks = append(asks, ask{service: service, r: r})
		}
	}
	slots := make(chan struct{}, max(parallel, 1))
	var wg sync.WaitGroup
	for i := range asks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			asks[i].children, asks[i].err = withChildren.ChildImports(ctx, asks[i].r)
		}()
	}
	wg.Wait()
	var failures []error
	all := make(map[string][]harvest.Resource, len(resourcesByService))
	for service, resources := range resourcesByService {
		all[service] = append([]harvest.Resource(nil), resources...)
	}
	for _, a := range asks {
		if a.err != nil {
			failures = append(failures, fmt.Errorf("%s: %s %s: %w", a.service, a.r.Address.Type, a.r.Observed.ID, a.err))
		}
		for _, c := range a.children {
			parents[tagKey(c)] = tagKey(a.r)
		}
		all[a.service] = append(all[a.service], a.children...)
	}
	return all, parents, failures
}

// childParallel is how many resources withChildImports asks for their
// children at once: as many as services list at once (--list-parallel),
// where the provider lets them, else one.
func childParallel(provider harvest.Provider, options Options) int {
	if c, ok := provider.(harvest.ProviderWithConcurrentListing); !ok || !c.ConcurrentListing() {
		return 1
	}
	return max(options.ListParallel, 1)
}

// BackendFileName holds a generated root's state backend.
const BackendFileName = "backend.tf"

// DefaultLayout lays output out as one root per state boundary:
// account (or subscription or project), then region or global.
const DefaultLayout = "{out}/{provider}/{account}/{region}/"

// rootLayouts returns the layout of the roots of each of patterns: the
// pattern, or DefaultLayout if empty, with {account} and {region} filled
// in from the provider, which is asked for its scope once.
func rootLayouts(ctx context.Context, provider harvest.Provider, patterns ...string) ([]string, error) {
	layouts := make([]string, len(patterns))
	var scope *strings.Replacer
	for i, pattern := range patterns {
		if pattern == "" {
			pattern = DefaultLayout
		}
		if !strings.Contains(pattern, "{account}") && !strings.Contains(pattern, "{region}") {
			layouts[i] = pattern
			continue
		}
		if scope == nil {
			account, region := "default", "default"
			if withScope, ok := provider.(harvest.ProviderWithScope); ok {
				var err error
				if account, region, err = withScope.Scope(ctx); err != nil {
					return nil, err
				}
			}
			scope = strings.NewReplacer("{account}", account, "{region}", region)
		}
		layouts[i] = scope.Replace(pattern)
	}
	return layouts, nil
}

// rootValues are what the placeholders of a --layout stand for in one
// root, besides {account} and {region}, which rootLayouts fills in
// first.
type rootValues struct {
	out, provider, service, env string
}

// rootDir returns the directory pattern names for the root of r: {out},
// {provider}, {service} and {env} replaced by r's values, cleaned. The
// values are put in at once, so one that holds a placeholder isn't
// replaced again; other placeholders are left as they are.
func rootDir(pattern string, r rootValues) string {
	return filepath.Clean(strings.NewReplacer(
		"{out}", r.out,
		"{provider}", r.provider,
		"{service}", r.service,
		"{env}", r.env,
	).Replace(pattern))
}

// resourceName is what a resource's block label is made from (see
// engine.Label): on AWS, its Name tag, which people read it by, such as
// shop-private-eu-west-2a for a subnet the lister names by its ID; else
// the name the lister gave it.
func resourceName(providerName string, r harvest.Resource, tags map[string]string) string {
	if name := strings.TrimSpace(tags["Name"]); providerName == "aws" && name != "" {
		return name
	}
	return r.RawName
}

// resourceNames returns what each resource's block label is made from:
// its resourceName, in which each word (as strings.Fields splits it) that
// is the ID of another resource listed or, as a child, in
// resourcesByService, becomes that resource's own name. So an
// association a lister names after the subnet it belongs to, by the
// subnet's ID, is labelled after the subnet's Name tag, and an Elastic IP
// named after its instance after the instance's. Where several
// resources have the ID, such as a bucket and its versioning, the name
// comes from the one of the first type in alphabetical order, among those
// whose name isn't their ID.
func resourceNames(providerName string, listed listing, resourcesByService map[string][]harvest.Resource) func(harvest.Resource) string {
	nameOf := func(r harvest.Resource) string { return resourceName(providerName, r, listed.tagsOf(r)) }
	type named struct{ resourceType, name string }
	byID := map[string]named{}
	add := func(resources map[string][]harvest.Resource) {
		for _, list := range resources {
			for _, r := range list {
				id, name := r.Observed.ID, nameOf(r)
				if id == "" || name == "" || name == id {
					continue
				}
				if seen, ok := byID[id]; !ok || r.Address.Type < seen.resourceType || (r.Address.Type == seen.resourceType && name < seen.name) {
					byID[id] = named{r.Address.Type, name}
				}
			}
		}
	}
	add(listed.resources)
	add(resourcesByService)
	return func(r harvest.Resource) string {
		name := nameOf(r)
		words := strings.Fields(name)
		resolved := false
		for i, word := range words {
			if other, ok := byID[word]; ok && word != r.Observed.ID {
				words[i] = other.name
				resolved = true
			}
		}
		if !resolved {
			return name
		}
		return strings.Join(words, " ")
	}
}
