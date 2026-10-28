// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/ratelimit"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
	"github.com/IgnatG/infraharvest/internal/version"
)

// listedCall is an Import call whose resources are listed, selected and
// grouped into roots, ready to generate (see listCall): the calls of one
// scope are all listed before any generates, so that each root refers to
// the roots of the scope's other calls (see ImportCalls).
type listedCall struct {
	run      *Run
	provider harvest.Provider
	// options are the call's, as the command gave them.
	options Options
	scope   string
	// here is what the call's listing found in its scope, if it can tell
	// what is gone (see listedInScope); only for --incremental.
	here *listedHere
	// byDir are the imports of each root the call generates.
	byDir map[string][]rootfile.Import
	// places are where each root of one of the configuration file's
	// environments goes when its stack merges (see mergePlaces), and
	// region is the scope's region.
	places map[string]memberPlace
	region string
	// merging are the roots of places their stack's merge is to take (see
	// markMerging).
	merging map[string]bool
	// handovers are what the call's roots hand over to the other roots of
	// the run, with --regenerate (see planHandovers).
	handovers *handovers
	// opts are the engine's options, without the other roots, and
	// toolchain what the roots are generated with.
	opts      engine.Options
	toolchain toolchain
}

// listCall lists, selects and groups into roots what one Import call
// imports into run (see importInto), recording what it found into run,
// stage by stage: it prepares the call (see Run.prepareCall), lists and
// selects its resources (see Run.listAndSelect), lays out its roots (see
// Run.layOut), gets the toolchain that generates them (see
// Run.toolchain), adds the selected resources' children, and places
// every resource in its root (see Run.place). It returns nil, with no
// error, for discover, which generates nothing.
func listCall(ctx context.Context, run *Run, provider harvest.Provider, options Options, args []string) (*listedCall, error) {
	if !run.used {
		// Should the import fail before it gets as far as the report, the
		// run still writes one there, with the failure (see Run.finish).
		run.options = options
	}
	// The provider signs in and lists with the run's limiter, not another
	// run's (see ratelimit.FromContext).
	ctx = ratelimit.NewContext(ctx, run.shared.rateLimiter(options.RateLimit))
	c, err := run.prepareCall(ctx, provider, options, args)
	if err != nil {
		return nil, err
	}
	listed, err := run.listAndSelect(ctx, provider, c, args)
	if err != nil {
		return nil, err
	}
	if options.Discover {
		run.recordDiscovery(c, listed)
		return nil, nil
	}
	layout, err := run.layOut(ctx, provider, c)
	if err != nil {
		return nil, err
	}
	tools, err := run.toolchain(ctx, provider, c, listed.selected)
	if err != nil {
		return nil, err
	}
	if err := writeGitignore(options.Out); err != nil {
		return nil, err
	}
	// Children follow their parent: only selected resources have them.
	var resources callResources
	var childFailures []error
	resources.byService, resources.parents, childFailures = withChildImports(ctx, provider, listed.selected, childParallel(provider, options))
	var here *listedHere
	if options.Incremental {
		var why string
		if here, why = listedInScope(provider, c, layout.pattern, listed.listed, listed.listFailures, resources.byService); here == nil {
			log.Printf("%s: the roots' resources this run doesn't find aren't checked for whether they are still in the cloud: %s (--incremental)", provider.Name(), why)
		}
	}
	opts := engineOptions(provider, c, listed, tools)
	opts.Modules, opts.ScannerSlots = &run.shared.modules, &run.shared.scanners
	placed, err := run.place(ctx, provider, c, layout, listed.listed, resources, opts.ModuleProviders)
	if err != nil {
		return nil, err
	}
	run.recordCall(c, listed, resources, tools, placed, slices.Concat(listed.failures, childFailures))
	if options.Incremental {
		// What an earlier import put in another root stays there.
		held, err := run.outputIndex(options.Out).heldElsewhere(placed.byDir)
		if err != nil {
			return nil, err
		}
		run.leaveOutHeld(placed.byDir, held)
	}
	return &listedCall{run: run, provider: provider, options: options, scope: c.scope, here: here, byDir: placed.byDir, places: placed.places, region: scopeRegion(c.scope), opts: opts, toolchain: *tools}, nil
}

// callContext is one Import call's Options, as the command gave them, and
// what the call works out from them before it lists (see
// Run.prepareCall). The stages after it read these, rather than rewrite
// the options, so the options of a call and of its run are always the
// command's.
type callContext struct {
	options Options
	// cat is the catalogue of the import, the built-in one with the
	// --catalogue overlay merged in; the provider of each service listed
	// is given it (see listServices).
	cat *catalogue.Catalogue
	// unwanted reports whether the import leaves out every resource of a
	// type whatever is listed (see selection.Unwanted), whose listing the
	// provider of each service listed may skip (see listServices); nil for
	// discover, which lists every type, so that the selection file can
	// include any.
	unwanted func(typ string) bool
	// services are the services the call lists: options.Services resolved
	// (see ResolveServices).
	services []string
	// scope is where the provider lists (see discoveryScope).
	scope string
	// importID returns the ID Terraform imports a resource by (see
	// importIDFunc).
	importID func(harvest.Resource) (string, bool)
	// query returns how the query service lists through a provider of its
	// own, given what the call's other services listed (see Run.query);
	// nil when the call doesn't list it.
	query func(provider harvest.Provider, listedByOthers []harvest.Resource) harvest.Query
	// root is the files every root of the call starts with, which pin
	// the provider, resolved once (see Run.callRoot).
	root struct {
		once  sync.Once
		files *rootFiles
		err   error
	}
}

// prepareCall initialises provider with args, and works out what the
// call with options lists: the catalogue, which the provider is given, the
// types the selection file leaves out, the services, and the scope, which
// r counts what it records by from now on.
func (r *Run) prepareCall(ctx context.Context, provider harvest.Provider, options Options, args []string) (*callContext, error) {
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
	c := &callContext{options: options, cat: cat}
	if !options.Discover {
		// An import needn't list what it leaves out anyway.
		chosen, err := r.selectionFile(options)
		if err != nil {
			return nil, err
		}
		c.unwanted = selection.Unwanted(cat, chosen)
	}
	c.services = ResolveServices(provider, options)
	c.scope = discoveryScope(ctx, provider)
	r.scope = c.scope
	c.importID = importIDFunc(cat)
	if usesQuery(options) {
		c.query = func(provider harvest.Provider, listedByOthers []harvest.Resource) harvest.Query {
			return r.query(c, provider, listedByOthers)
		}
	}
	return c, nil
}

// callListing is what an Import call listed in its scope, and what of it
// its selection keeps (see Run.listAndSelect).
type callListing struct {
	listed listing
	// listFailures are what failed to list; failures are those, then what
	// failed after them, such as the default selection.
	listFailures, failures []error
	defaults               *selection.Sources
	// selected are the resources the selection keeps, by service, and
	// leftOut those it leaves out, which the roots may refer to (see
	// Run.selectResources). Discover selects none.
	selected map[string][]harvest.Resource
	leftOut  []engine.External
}

// listAndSelect lists the resources of the call c, with args, leaves out
// those Terraform already manages (see excludeManaged) and those outside
// the import's scope (see Run.applyImportScope), and, but for discover,
// selects what to import of the rest (see Run.selectResources).
func (r *Run) listAndSelect(ctx context.Context, provider harvest.Provider, c *callContext, args []string) (*callListing, error) {
	listed, failures, err := listResources(ctx, provider, c, args)
	if err != nil {
		return nil, err
	}
	l := &callListing{listed: listed, listFailures: failures, failures: failures}
	if l.defaults, err = defaultSelection(ctx, provider, c.cat, listed, c.options.IncludeOwned); err != nil {
		l.failures = append(l.failures, fmt.Errorf("default selection: %w", err))
	}
	if err := excludeManaged(ctx, r, c.options, listed.resources, l.defaults, c.importID); err != nil {
		return nil, err
	}
	// A scope that can't be applied stops the call: going on would import
	// every resource listed, not only those of the scope.
	if err := r.applyImportScope(ctx, provider, listed, l.defaults, c.scope); err != nil {
		return nil, err
	}
	if c.options.Discover {
		return l, nil
	}
	chosen, err := r.selectionFile(c.options)
	if err != nil {
		return nil, err
	}
	l.selected, l.leftOut = r.selectResources(listed, l.defaults, chosen, c.scope, provider.Profile().NameTag, c.importID)
	return l, nil
}

// recordDiscovery records into r what discover listed in the call c, l,
// for the selection file it writes (see Run.finish).
func (r *Run) recordDiscovery(c *callContext, l *callListing) {
	r.used = true
	r.options = c.options
	r.failures = append(r.failures, l.failures...)
	r.addDiscovered(l.listed, l.defaults, c.scope, c.importID)
	r.cover(c)
}

// callLayout is where an Import call's roots go (see Run.layOut).
type callLayout struct {
	// envNames are the configuration file's environments of the call's
	// scope, if any; none keeps the layout as without them.
	envNames []string
	// pattern is the call's --layout, with {env} where environments split
	// its roots (see withEnvLayout), and root the layout of the roots,
	// pattern with the call's account and region in (see rootLayouts).
	pattern, root string
	// unsplit is where the roots would be without the file's
	// environments, for those an earlier import generated there, which
	// keep their resources, or with --regenerate hand them over (see
	// keptRoots).
	unsplit string
}

// layOut works out where the roots of the call c go, by its --layout,
// --env-tags and the configuration file's environments of its scope.
func (r *Run) layOut(ctx context.Context, provider harvest.Provider, c *callContext) (callLayout, error) {
	options := c.options
	l := callLayout{envNames: r.environments.For(provider.Profile(), scopeAccount(c.scope))}
	r.warnUnscoped(provider, c.scope)
	l.pattern = withEnvLayout(options.Layout, len(options.EnvTags) > 0 || len(l.envNames) > 0)
	layouts, err := rootLayouts(ctx, provider, l.pattern, withEnvLayout(options.Layout, len(options.EnvTags) > 0))
	if err != nil {
		return callLayout{}, err
	}
	l.root, l.unsplit = layouts[0], layouts[1]
	return l, nil
}

// toolchain is what an Import call generates its roots with (see
// Run.toolchain): the engine binary, the files every root starts with,
// and the modules that hold clusters of resources.
type toolchain struct {
	binary             tfrun.Binary
	execPath, cacheDir string
	root               *rootFiles
	// adapters are those of the modules the roots may call, and modulesDir
	// where generated local modules go, by the call's --modules mode (see
	// moduleOptions).
	adapters   []adapters.Adapter
	modulesDir string
	// newer are the modules called below their newest release, and unused
	// those not called, as they accept only an older major release of the
	// provider, for the report.
	newer  []report.ModuleVersion
	unused []report.UnusedModule
}

// toolchain finds or installs the engine binary of the call c, resolves
// the modules its --modules mode (see modulesMode) may call for the
// selected resources, the organisation's own first, and renders the files
// every root starts with, which pin the provider to a release those
// modules accept (see rootConfig), unless the query service pinned its
// listing with them already (see Run.callRoot).
func (r *Run) toolchain(ctx context.Context, provider harvest.Provider, c *callContext, selected map[string][]harvest.Resource) (*toolchain, error) {
	options := c.options
	t := &toolchain{binary: engineBinaryOf(options)}
	var err error
	if t.cacheDir, err = infraharvestCacheDir(); err != nil {
		return nil, err
	}
	if t.execPath, err = r.tools.find(ctx, t.binary, options.TerraformPath, filepath.Join(t.cacheDir, t.binary.Name)); err != nil {
		return nil, err
	}
	mode := modulesMode(options)
	if options.stackScoped() && mode != ModulesNone {
		log.Printf("--modules %s with --cfn-stack: the stacks' resources that go into module calls don't take their templates' parameters and outputs; --modules none keeps them in the roots, which do", mode)
	}
	var resolved []adapters.Adapter
	var constraints []moduleConstraint
	if modulesFromRegistry(mode) {
		if mode == ModulesLatestUntested {
			log.Printf("warning: --modules %s calls module releases no adapter is tested with: terraform init downloads them, and plan runs their code with the credentials this import uses; review what they call before you rely on the output", ModulesLatestUntested)
		}
		resolved, t.newer, constraints = r.registryModules(ctx, provider, mode, selected)
		// The organisation's own modules come first.
		own := applicableAdapters(catalogueAdapters(r.shared.ownAdapters(ctx, t.execPath, filepath.Join(t.cacheDir, "modules"), provider.Name(), r.modules), provider.Name()), selected)
		resolved = append(own, resolved...)
	}
	if t.root, err = r.callRoot(ctx, c, t.binary.Registry, t.execPath, engineProvider(provider), constraints); err != nil {
		return nil, err
	}
	for _, m := range t.root.refused {
		t.unused = append(t.unused, report.UnusedModule{Source: m.module, Version: m.version, Accepts: m.constraint.String()})
	}
	t.adapters, t.modulesDir = moduleOptions(mode, options.Out, withoutModules(resolved, t.root.refused))
	return t, nil
}

// registryModules resolves the curated modules of provider, from the
// registry, that --modules mode may call for the resources of byService
// (see resolveAdapters): the adapters, those called below their newest
// release, and the provider releases each accepts. A mode that calls no
// module from the registry has none.
func (r *Run) registryModules(ctx context.Context, provider harvest.Provider, mode string, byService map[string][]harvest.Resource) ([]adapters.Adapter, []report.ModuleVersion, []moduleConstraint) {
	if !modulesFromRegistry(mode) {
		return nil, nil, nil
	}
	return resolveAdapters(ctx, r.moduleReleases, applicableAdapters(adapters.For(provider.Profile().Modules), byService), engineProvider(provider).Source, mode == ModulesLatestUntested)
}

// moduleReleases returns the releases of the registry module at source,
// looked up once for the whole command (see moduleReleases).
func (r *Run) moduleReleases(ctx context.Context, source string) ([]engine.ModuleRelease, error) {
	return r.shared.releases.get(ctx, source, func(ctx context.Context) ([]engine.ModuleRelease, error) {
		return r.tools.listModuleReleases(ctx, source)
	})
}

// callRoot renders, once for the call c, the files every root it
// generates starts with, which pin the provider p to a release the
// modules' constraints accept (see rootConfig). The query service lists
// with that pin too, so that Terraform lists with the provider release
// the roots import with: it resolves the pin first, from the modules of
// what the call's other services listed, and the toolchain then takes
// it as it is.
func (r *Run) callRoot(ctx context.Context, c *callContext, registry, execPath string, p rootfile.Provider, constraints []moduleConstraint) (*rootFiles, error) {
	c.root.once.Do(func() {
		c.root.files, c.root.err = r.tools.root(ctx, registry, execPath, p, constraints)
	})
	return c.root.files, c.root.err
}

// engineOptions configures engine.Generate for the roots of the call c, in
// one place: what the catalogue knows about their types, for the provider
// release the toolchain t pins (see catalogueEngineOptions);
// t's root files and modules; what the provider's profile says of tags
// and of the arguments resources inherit, and the other providers the
// roots may configure; the flags the engine takes; and what the selection
// of l leaves out, or keeps although another tool owns it.
func engineOptions(provider harvest.Provider, c *callContext, l *callListing, t *toolchain) engine.Options {
	opts := catalogueEngineOptions(c.cat, t.root.version)
	opts.Config, opts.Scanners, opts.SplitFiles, opts.TidyLabels = t.root.files, gate.DefaultScanners(c.options.Policies), true, true
	opts.Adapters, opts.ModulesDir = t.adapters, t.modulesDir
	opts.IncludeOwned, opts.CommandTimeout = c.options.IncludeOwned, c.options.EngineTimeout
	opts.KeepOwned = keptOwned(l.selected, l.listed, c.importID)
	opts.External = l.leftOut
	profile := provider.Profile()
	if tags := profile.DefaultTags; tags != nil {
		opts.DefaultTags = &engine.DefaultTags{Provider: provider.Name(), Attribute: tags.Attribute, Block: tags.Block, Argument: tags.Argument, ReservedPrefix: tags.ReservedPrefix}
	}
	if tags := profile.EffectiveTags; tags != nil {
		opts.EffectiveTags = (*engine.EffectiveTags)(tags)
	}
	opts.Inherited = profile.InheritedArguments
	if withModuleProviders, ok := provider.(harvest.ProviderWithModuleProviders); ok {
		opts.ModuleProviders = map[string]rootfile.Provider{}
		for name, p := range withModuleProviders.ModuleProviders() {
			if name != provider.Name() {
				opts.ModuleProviders[name] = rootfile.Provider{Name: name, Source: p.Source, Config: p.Config}
			}
		}
	}
	return opts
}

// callResources are the resources an Import call imports: those its
// selection keeps, with the children the provider lists for them (see
// withChildImports), by service, and each child's parent, by tagKey.
type callResources struct {
	byService map[string][]harvest.Resource
	parents   map[string]string
}

// placement is where an Import call's resources go (see Run.place).
type placement struct {
	// byDir are the imports of each root, and skipped counts by type the
	// resources Terraform can't import.
	byDir   map[string][]rootfile.Import
	skipped map[string]int
	// kept are, with the configuration file's environments, the roots an
	// earlier import generated without them, which keep their resources
	// (see keptRoots), and places where each root goes when its stack
	// merges, with --merge-environments (see mergePlaces).
	kept   *keptRoots
	places map[string]memberPlace
}

// place puts each of the resources of the call c in its root, by layout:
// the environment of each, by its tags, which listed has, or by the
// configuration file's environments; the roots earlier imports generated
// that keep theirs; its import block (see importsByDir), which leaves
// out the provider where its type implies one of configured; and where
// each root goes when its stack merges.
func (r *Run) place(ctx context.Context, provider harvest.Provider, c *callContext, layout callLayout, listed listing, resources callResources, configured map[string]rootfile.Provider) (*placement, error) {
	options := c.options
	unplaced := func(harvest.Resource) string { return "" }
	byEnvTags := func(pattern string) func(harvest.Resource) string {
		if !strings.Contains(pattern, "{env}") {
			return unplaced
		}
		keys := options.EnvTags
		if len(keys) == 0 {
			keys = defaultEnvTags
		}
		envs := environments(resources.byService, listed.tags, byTagKeys(keys), resources.parents, c.importID, untaggedEnv(options.DefaultEnv))
		return func(res harvest.Resource) string { return envs[tagKey(res)] }
	}
	envOf := byEnvTags(layout.root)
	if len(layout.envNames) > 0 {
		envOf = configuredEnvOf(r.environments, layout.envNames, resources.byService, listed.tags, resources.parents, c.importID)
	}
	r.warnUnreadEnvTags(provider, layout, options.DefaultEnv, resources.byService[harvest.QueryService])
	dirOf := layoutDirs(layout.root, options.Out, providerDir(provider), envOf)
	p := &placement{}
	if len(layout.envNames) > 0 {
		p.kept = newKeptRoots(r.outputIndex(options.Out), r.shared)
		unsplit := layoutDirs(layout.unsplit, options.Out, providerDir(provider), byEnvTags(layout.unsplit))
		if err := p.kept.split(resources.byService, dirOf, unsplit, c.importID, options.Regenerate); err != nil {
			return nil, err
		}
		dirOf = p.kept.dirs(dirOf, unsplit, envOf)
	}
	profile := provider.Profile()
	blocks := importBlocks{
		provider:   provider.Name(),
		configured: configured,
		cat:        c.cat,
		importID:   c.importID,
		nameOf:     resourceNames(profile.NameTag, listed, resources.byService),
		fileOf: func(service string, res harvest.Resource) string {
			return configFile(profile, service, res.Address.Type, c.cat)
		},
	}
	p.byDir, p.skipped = importsByDir(blocks, resources.byService, dirOf)
	if options.MergeEnvironments && len(layout.envNames) > 0 {
		stackLayouts, err := rootLayouts(ctx, provider, stackLayout(layout.pattern))
		if err != nil {
			return nil, err
		}
		p.places = mergePlaces(resources.byService, envOf, dirOf, layoutDirs(stackLayouts[0], options.Out, providerDir(provider), envOf), p.kept, p.byDir)
	}
	return p, nil
}

// recordCall records into r what the call c did: the options it ran
// with, the roots it keeps, the manifest of its toolchain t, its
// failures, the resources Terraform can't import, and the count of what
// it discovered: those the selection of l left out, and the resources it
// imports.
func (r *Run) recordCall(c *callContext, l *callListing, resources callResources, t *toolchain, p *placement, failures []error) {
	r.used = true
	r.options = c.options
	r.report.CountByTags(c.options.ReportTags)
	if p.kept != nil {
		r.report.AddKeptRoots(p.kept.report(c.scope))
	}
	// Each call adds what it selected: another region's may hold the
	// provider back, or find other modules.
	r.report.Add(report.Manifest{
		Tool:     report.Component{Name: "infraharvest", Version: version.Version},
		Engine:   report.Component{Name: t.binary.Name, Version: t.root.engineVersion},
		Provider: report.Provider{Source: qualifiedSource(t.binary.Registry, t.root.provider.Source), Constraint: t.root.provider.Version, HeldBack: t.root.heldBack},
		Modules:  t.newer,
		Unused:   t.unused,
	})
	for _, f := range failures {
		r.report.AddFailure(f)
	}
	r.failures = append(r.failures, failures...)
	for typ, n := range p.skipped {
		r.skipped[typ] += n
	}
	// What the selection left out, and what it keeps with their children.
	counted := len(l.leftOut)
	for typ, n := range discoveredByType(resources.byService) {
		r.discovered[typ] += n
		counted += n
	}
	if c.scope != "" {
		r.report.AddDiscovered(c.scope, counted)
	}
}
