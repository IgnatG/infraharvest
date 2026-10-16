// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	goversion "github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-exec/tfexec"

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
		return run.finish(importInto(ctx, run, provider, options, args))
	}
	return importInto(ctx, run, provider, options, args)
}

// importInto imports into run. It returns an error only if the import
// couldn't run; what couldn't be imported goes into run.
func importInto(ctx context.Context, run *Run, provider harvest.Provider, options Options, args []string) error {
	binary := engineBinary(options.Engine)
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	// Before the provider signs in: its clients take the rate.
	ratelimit.SetDefault(options.RateLimit)
	if err := provider.Init(ctx, args); err != nil {
		return err
	}
	options = ResolveServices(provider, options)
	scope := discoveryScope(ctx, provider)
	run.scope = scope
	cat, err := providerCatalogue(provider, options.Catalogue)
	if err != nil {
		return err
	}
	if p, ok := provider.(harvest.ProviderWithCatalogue); ok {
		p.SetCatalogue(cat)
	}
	importID := importIDFunc(cat)
	listed, failures, err := listResources(ctx, provider, options, args, scope)
	if err != nil {
		return err
	}
	defaults, err := excludedByDefault(ctx, provider, cat, listed.resources)
	if err != nil {
		failures = append(failures, fmt.Errorf("default selection: %w", err))
	}
	if defaults, err = excludeManaged(ctx, run, options, listed.resources, defaults, importID); err != nil {
		return err
	}
	if !options.IncludeOwned {
		defaults = excludeOwned(listed, defaults)
	}
	if options.Discover {
		run.used = true
		run.options = options
		run.failures = append(run.failures, failures...)
		run.addDiscovered(listed, defaults, scope, importID)
		return nil
	}
	chosen, err := run.selectionFile(options.Selection)
	if err != nil {
		return err
	}
	selected, leftOut := run.selectResources(listed, defaults, chosen, scope, importID)
	if options.Layout, err = rootLayout(ctx, provider, withEnvLayout(options.Layout, options.EnvTags)); err != nil {
		return err
	}

	cacheDir, err := infraharvestCacheDir()
	if err != nil {
		return err
	}
	execPath, err := binary.Find(ctx, options.TerraformPath, filepath.Join(cacheDir, binary.Name))
	if err != nil {
		return err
	}
	var moduleAdapters []adapters.Adapter
	var newerModules []report.ModuleVersion
	var constraints []moduleConstraint
	if options.Modules == "" || options.Modules == ModulesRegistry || options.Modules == ModulesLatestUntested {
		moduleAdapters, newerModules, constraints = resolveAdapters(ctx, http.DefaultClient, "", applicableAdapters(adapters.For(provider.Name()), selected), engineProvider(provider).Source, options.Modules == ModulesLatestUntested)
		// The organisation's own modules come first.
		own := applicableAdapters(catalogueAdapters(ctx, execPath, run.modules, provider.Name()), selected)
		moduleAdapters = append(own, moduleAdapters...)
	}
	root, err := rootConfig(ctx, http.DefaultClient, binary.Registry, "", execPath, engineProvider(provider), constraints)
	if err != nil {
		return err
	}
	if err := writeGitignore(options.Out); err != nil {
		return err
	}

	// Children follow their parent: only selected resources have them.
	resourcesByService, parents, childFailures := withChildImports(ctx, provider, selected)
	failures = append(failures, childFailures...)
	opts := engineOptions(provider, cat, root)
	opts.IncludeOwned = options.IncludeOwned
	opts.KeepOwned = keptOwned(selected, listed, importID)
	opts.External = leftOut
	switch options.Modules {
	case "", ModulesRegistry, ModulesLatestUntested:
		opts.Adapters = moduleAdapters
		opts.ModulesDir = filepath.Join(options.Out, engine.ModulesDirName)
	case ModulesLocal:
		opts.ModulesDir = filepath.Join(options.Out, engine.ModulesDirName)
	}
	envOf := func(harvest.Resource) string { return "" }
	if strings.Contains(options.Layout, "{env}") {
		keys := options.EnvTags
		if len(keys) == 0 {
			keys = defaultEnvTags
		}
		envs := environments(resourcesByService, listed.tags, keys, parents, importID)
		envOf = func(r harvest.Resource) string { return envs[tagKey(r)] }
	}
	nameOf := resourceNames(provider.Name(), listed, resourcesByService)
	fileOf := func(service string, r harvest.Resource) string {
		return configFile(provider.Name(), service, r.Address.Type, cat)
	}
	byDir, skipped := importsByDir(providerDir(provider), provider.Name(), options, resourcesByService, importID, envOf, nameOf, fileOf)

	run.used = true
	run.options = options
	run.report.TagKeys = options.ReportTags
	run.report.Manifest = report.Manifest{
		Tool:     report.Component{Name: "infraharvest", Version: version.Version},
		Engine:   report.Component{Name: binary.Name, Version: root.engineVersion},
		Provider: report.Provider{Source: qualifiedSource(binary.Registry, root.provider.Source), Constraint: root.provider.Version, HeldBack: root.heldBack},
		Modules:  newerModules,
	}
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

	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		dirOpts := withOtherRoots(opts, byDir, dir)
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
		}
		incremental := false
		if result == nil && options.Incremental {
			if incremental, err = engine.HasConfiguration(dir); err != nil {
				return err
			}
		}
		switch {
		case result != nil:
			log.Printf("%s: %s is unchanged since it was generated (--resume)", provider.Name(), dir)
			if run.lock == nil {
				run.lock, _ = os.ReadFile(filepath.Join(dir, engine.LockFileName))
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
			if err == nil && addedResult == nil {
				continue // nothing new
			}
			run.addDirectory(dir, added, addedResult, err)
			continue
		default:
			if options.Resume {
				// What changed is generated again, from scratch.
				if err := clearGenerated(options.Out, dir); err != nil {
					return err
				}
			}
			log.Printf("%s: generating configuration for %d resources in %s", provider.Name(), len(byDir[dir]), dir)
			result, err = generateDir(ctx, dir, tr, byDir[dir], dirOpts, &run.lock)
			if err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil {
				if err := saveCheckpoint(options.Out, dir, fp, result); err != nil {
					return err
				}
			}
		}
		run.addDirectory(dir, byDir[dir], result, err)
		if err == nil && run.backend != nil {
			// After the checks, which run on a local working directory: the
			// import never needs access to the state backend.
			if err := os.WriteFile(filepath.Join(dir, BackendFileName), run.backend.File(relativePath(options.Out, dir)), 0o644); err != nil {
				return err
			}
		}
	}
	run.report.Provider.Version = engine.LockedVersion(run.lock, run.report.Provider.Source)
	return nil
}

// engineOptions configures engine.Generate for provider's directories,
// with what cat knows about its types.
func engineOptions(provider harvest.Provider, cat *catalogue.Catalogue, root *rootFiles) engine.Options {
	opts := engine.Options{Config: root.files, Scanners: engine.DefaultScanners(), Omit: cat.Omit(), Defaults: cat.Defaults(), StateOnly: cat.StateOnly(), SplitFiles: true, TidyLabels: true}
	if sources := cat.DataSources(); len(sources) > 0 {
		opts.DataSources = map[string]engine.DataSource{}
		for typ, d := range sources {
			opts.DataSources[typ] = engine.DataSource{Type: d.Type, Argument: d.Argument}
		}
	}
	if placeholders := cat.Placeholders(); len(placeholders) > 0 {
		opts.Placeholders = map[string]engine.Placeholder{}
		for typ, p := range placeholders {
			opts.Placeholders[typ] = engine.Placeholder{OneOf: p.OneOf, Set: p.Set, Ignore: p.Ignore, Reason: p.Reason}
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
	reported.Imported = make([]report.Resource, 0, len(result.Imported))
	for _, imp := range result.Imported {
		reported.Imported = append(reported.Imported, report.Resource{Address: imp.Type + "." + imp.Name, ID: imp.ID})
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
		reported.Checks = append(reported.Checks, report.Check{Name: c.Name, Passed: c.Passed, Details: c.Details})
		if !c.Passed {
			r.failures = append(r.failures, fmt.Errorf("%s: %s failed: %s", dir, c.Name, strings.Join(c.Details, "; ")))
		}
	}
	if len(result.Secrets) > 0 {
		log.Printf("%s: set %d secret variables before planning (see %s)", dir, len(result.Secrets), engine.VariablesFileName)
	}
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
// as fresh credentials (see terraformEnv).
type terraformRun struct {
	execPath, pluginCacheDir string
	env                      map[string]string
}

func (t terraformRun) in(dir string) (*tfexec.Terraform, error) {
	return engine.NewTerraform(dir, t.execPath, t.pluginCacheDir, t.env)
}

// terraformEnv returns what Terraform needs in its environment to import
// provider's resources, such as the credentials it lists with (see
// harvest.ProviderWithTerraformEnv), fresh for each root.
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

// rootFiles are the files every output directory starts with, and the
// versions they pin.
type rootFiles struct {
	files         map[string][]byte // versions.tf and providers.tf
	engineVersion string
	provider      engine.Provider // Version is the constraint
	// heldBack says which modules keep the provider below its newest
	// release, if any do.
	heldBack string
}

// rootConfig renders versions.tf and providers.tf for every output
// directory: required_version within the major release of the Terraform or
// OpenTofu at execPath, and the provider pinned to its newest minor release
// line in registry (the engine's default registry), unless its source names
// another: the newest release the module versions the import may call
// accept (constraints). A registryURL replaces the registry, for tests.
func rootConfig(ctx context.Context, client *http.Client, registry, registryURL, execPath string, p engine.Provider, constraints []moduleConstraint) (*rootFiles, error) {
	engineVersion, err := engine.BinaryVersion(ctx, execPath)
	if err != nil {
		return nil, err
	}
	latest, err := engine.LatestProviderVersion(ctx, client, registryURL, qualifiedSource(registry, p.Source))
	if err != nil {
		return nil, err
	}
	chosen, held := latest, ""
	if len(constraints) > 0 {
		all := make([]goversion.Constraints, len(constraints))
		for i, c := range constraints {
			all[i] = c.constraint
		}
		v, err := engine.ProviderVersion(ctx, client, registryURL, qualifiedSource(registry, p.Source), all)
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
			engine.VersionsFileName:  engine.VersionsFile(engine.RequiredVersion(engineVersion), p),
			engine.ProvidersFileName: providers,
		},
		engineVersion: engineVersion.String(),
		provider:      p,
		heldBack:      held,
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

// importsByDir groups resources into import blocks per output directory,
// following --layout with dirName for {provider}, each named by nameOf and
// in the file fileOf names. providerName is the Terraform provider the
// roots declare. It leaves out, logs and counts by type resources Terraform
// can't import.
func importsByDir(dirName, providerName string, options Options, resourcesByService map[string][]harvest.Resource, importID func(harvest.Resource) (string, bool), envOf, nameOf func(harvest.Resource) string, fileOf func(service string, r harvest.Resource) string) (map[string][]engine.Import, map[string]int) {
	byDir := map[string][]engine.Import{}
	skipped := map[string]int{}
	for service, resources := range resourcesByService {
		for _, r := range resources {
			dir := rootDir(options.Layout, rootValues{out: options.Out, provider: dirName, service: service, env: envOf(r)})
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

// withOtherRoots returns opts for the root in dir, with what the other
// roots of the import manage as external: references to it read it, as
// references between roots do.
func withOtherRoots(opts engine.Options, byDir map[string][]engine.Import, dir string) engine.Options {
	if len(byDir) < 2 {
		return opts
	}
	dirOpts := opts
	dirOpts.External = slices.Clone(opts.External)
	for other, imports := range byDir {
		if other == dir {
			continue
		}
		for _, imp := range imports {
			dirOpts.External = append(dirOpts.External, engine.External{Type: imp.Type, ID: imp.ID})
		}
	}
	slices.SortFunc(dirOpts.External, func(a, b engine.External) int {
		return cmp.Or(strings.Compare(a.Type, b.Type), strings.Compare(a.ID, b.ID))
	})
	dirOpts.External = slices.Compact(dirOpts.External)
	return dirOpts
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
// couldn't list children for as failures.
func withChildImports(ctx context.Context, provider harvest.Provider, resourcesByService map[string][]harvest.Resource) (map[string][]harvest.Resource, map[string]string, []error) {
	parents := map[string]string{}
	withChildren, ok := provider.(harvest.ProviderWithChildImports)
	if !ok {
		return resourcesByService, parents, nil
	}
	var failures []error
	all := make(map[string][]harvest.Resource, len(resourcesByService))
	for service, resources := range resourcesByService {
		all[service] = append([]harvest.Resource(nil), resources...)
		for _, r := range resources {
			children, err := withChildren.ChildImports(ctx, r)
			if err != nil {
				failures = append(failures, fmt.Errorf("%s: %s %s: %w", service, r.Address.Type, r.Observed.ID, err))
			}
			for _, c := range children {
				parents[tagKey(c)] = tagKey(r)
			}
			all[service] = append(all[service], children...)
		}
	}
	return all, parents, failures
}

// BackendFileName holds a generated root's state backend.
const BackendFileName = "backend.tf"

// DefaultLayout lays output out as one root per state boundary:
// account (or subscription or project), then region or global.
const DefaultLayout = "{out}/{provider}/{account}/{region}/"

// rootLayout returns the layout of the roots: pattern, or
// DefaultLayout if empty, with {account} and {region} filled in
// from the provider.
func rootLayout(ctx context.Context, provider harvest.Provider, pattern string) (string, error) {
	if pattern == "" {
		pattern = DefaultLayout
	}
	if !strings.Contains(pattern, "{account}") && !strings.Contains(pattern, "{region}") {
		return pattern, nil
	}
	account, region := "default", "default"
	if withScope, ok := provider.(harvest.ProviderWithScope); ok {
		var err error
		if account, region, err = withScope.Scope(ctx); err != nil {
			return "", err
		}
	}
	return strings.NewReplacer("{account}", account, "{region}", region).Replace(pattern), nil
}

// rootValues are what the placeholders of a --layout stand for in one
// root, besides {account} and {region}, which rootLayout fills in
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
