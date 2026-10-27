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
	"github.com/IgnatG/infraharvest/internal/report"
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

// modulesMode returns the --modules mode an import runs with: for one
// scoped to CloudFormation stacks (AWS --cfn-stack), ModulesNone unless
// --modules was chosen, as the stacks' templates are carried into the
// roots by the addresses of the resource blocks there (see stackintent),
// which a module call would hide: no parameter would become a variable,
// and no output be written.
func modulesMode(options Options) string {
	if len(options.CloudFormationStacks) > 0 && !options.ModulesChosen {
		return ModulesNone
	}
	return options.Modules
}

// engineBinary is the binary --engine runs.
func engineBinary(name string) engine.Binary {
	if name == EngineTofu {
		return engine.TofuBinary
	}
	return engine.TerraformBinary
}

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
			run.lock, _ = os.ReadFile(filepath.Join(rootSource(out, dir), engine.LockFileName))
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
			other[engine.ResourceKey(imp.Type, imp.ID)] = relativePath(out, d)
		}
	}
	return other
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

// importBlocks says how importsByDir writes the import block of a
// resource.
type importBlocks struct {
	// provider is the Terraform provider the roots declare, and configured
	// the other providers they may configure (see
	// engine.Options.ModuleProviders).
	provider   string
	configured map[string]engine.Provider
	// cat says what the configuration of a resource leaves out depending
	// on what its lister recorded, and the ID to import it by should
	// Terraform reject its own; nil says neither.
	cat      *catalogue.Catalogue
	importID func(harvest.Resource) (string, bool)
	// nameOf names a resource's block, and fileOf the file of its root
	// that it goes in.
	nameOf func(harvest.Resource) string
	fileOf func(service string, r harvest.Resource) string
}

// importsByDir groups resources into import blocks per output directory,
// the root dirOf names for each, each block whole, as blocks says (see
// importBlocks.of). It leaves out, logs and counts by type resources
// Terraform can't import.
func importsByDir(blocks importBlocks, resourcesByService map[string][]harvest.Resource, dirOf func(service string, r harvest.Resource) string) (map[string][]engine.Import, map[string]int) {
	byDir := map[string][]engine.Import{}
	skipped := map[string]int{}
	for service, resources := range resourcesByService {
		for _, r := range resources {
			dir := dirOf(service, r)
			imp, ok := blocks.of(service, r)
			if !ok {
				skipped[r.Address.Type]++
				continue
			}
			byDir[dir] = append(byDir[dir], imp)
		}
	}
	types := make([]string, 0, len(skipped))
	for typ := range skipped {
		types = append(types, typ)
	}
	sort.Strings(types)
	for _, typ := range types {
		log.Printf("%s: skipping %d %s: Terraform can't import this resource type", blocks.provider, skipped[typ], typ)
	}
	return byDir, skipped
}

// of returns the import block of r, a resource service listed, or false
// if Terraform can't import it. The block names the provider unless its
// type implies it (see importProvider), or one of the other providers
// the root may configure, such as azapi for an azapi_resource: the root
// requires that provider under its own name when it imports such a
// resource, so Terraform infers it from the type. It leaves out the
// arguments the catalogue leaves out depending on what r's lister
// recorded (see catalogue.Catalogue.ResourceOmit), and falls back on the
// catalogue's next import ID template for r, where that is the same
// resource by another query alone, as an azapi_resource's ID without
// ?api-version= is (see engine.Import.Fallback and engine.ResourceKey).
func (b importBlocks) of(service string, r harvest.Resource) (engine.Import, bool) {
	typ := r.Address.Type
	id, ok := b.importID(r)
	if !ok {
		return engine.Import{}, false
	}
	imp := engine.Import{Type: typ, Name: b.nameOf(r), File: b.fileOf(service, r), ID: id, Provider: importProvider(b.provider, typ)}
	implied, _, _ := strings.Cut(typ, "_")
	// google --beta's roots configure google too, for modules: its own
	// google_* resources still name google-beta.
	if _, ok := b.configured[implied]; ok && implied != catalogueName(b.provider) {
		imp.Provider = ""
	}
	if b.cat == nil || r.Observed == nil {
		return imp, true
	}
	if extra := b.cat.ResourceOmit(typ, r.Observed.Attributes); len(extra) > 0 {
		imp.Omit = extra
	}
	if fallback, ok := b.cat.FallbackImportID(typ, r.Observed.ID, r.Observed.Attributes); ok && engine.ResourceKey(typ, fallback) == engine.ResourceKey(typ, id) {
		imp.Fallback = fallback
	}
	return imp, true
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
	return engine.ResourceKey(a.Type, a.ID) == engine.ResourceKey(b.Type, b.ID)
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
// harvest.Profile.Command), else its Terraform provider's name.
func providerDir(provider harvest.Provider) string {
	if command := provider.Profile().Command; command != "" {
		return command
	}
	return provider.Name()
}

// engineProvider describes the provider for the generated configuration.
func engineProvider(provider harvest.Provider) engine.Provider {
	name := provider.Name()
	source := provider.Profile().Source
	if source == "" {
		source = "hashicorp/" + name
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
	if !provider.Profile().ConcurrentListing {
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
// engine.Label): the value of the provider's name tag (see
// harvest.Profile.NameTag), which people read it by, such as AWS's Name
// tag shop-private-eu-west-2a for a subnet the lister names by its ID;
// else the name the lister gave it.
func resourceName(nameTag string, r harvest.Resource, tags map[string]string) string {
	if name := strings.TrimSpace(tags[nameTag]); nameTag != "" && name != "" {
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
func resourceNames(nameTag string, listed listing, resourcesByService map[string][]harvest.Resource) func(harvest.Resource) string {
	nameOf := func(r harvest.Resource) string { return resourceName(nameTag, r, listed.tagsOf(r)) }
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
