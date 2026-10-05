// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/adapters"
	"github.com/IgnatG/infraharvest/engine"
	"github.com/IgnatG/infraharvest/report"
	"github.com/IgnatG/infraharvest/terraformutils"
)

// Import engines selectable with --engine.
const (
	engineTerraform = "terraform"
	engineTofu      = "tofu"
)

// Values of --output: json also prints the import report (see package
// report) on stdout.
const (
	outputHCL  = "hcl"
	outputJSON = "json"
)

// Values of --modules: which modules hold clusters of resources.
const (
	// modulesRegistry tries curated public modules, then generated local
	// modules.
	modulesRegistry = "registry"
	// modulesLocal uses generated local modules only, such as where the
	// registry can't be reached.
	modulesLocal = "local"
	// modulesNone keeps every resource in the root.
	modulesNone = "none"
)

// engineBinary is the binary --engine runs.
func engineBinary(name string) engine.Binary {
	if name == engineTofu {
		return engine.TofuBinary
	}
	return engine.TerraformBinary
}

// importWithEngine lists resources with the provider's listers, then lets
// Terraform or OpenTofu generate their configuration from import blocks.
// No state is written.
func importWithEngine(provider terraformutils.ProviderGenerator, options ImportOptions, args []string) error {
	if err := checkImportOptions(options); err != nil {
		return err
	}
	if err := checkSelectionOptions(options); err != nil {
		return err
	}
	// Commands share one run across their Import calls (see withEngineRun);
	// a call on its own finishes its own.
	run := activeRun
	if run == nil {
		run = newEngineRun()
		return run.finish(importInto(run, provider, options, args))
	}
	return importInto(run, provider, options, args)
}

// importInto imports into run. It returns an error only if the import
// couldn't run; what couldn't be imported goes into run.
func importInto(run *engineRun, provider terraformutils.ProviderGenerator, options ImportOptions, args []string) error {
	binary := engineBinary(options.Engine)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := provider.Init(args); err != nil {
		return err
	}
	options = resolveServices(provider, options)
	listed, failures, err := listResources(ctx, provider, options, args)
	if err != nil {
		return err
	}
	defaults, err := excludedByDefault(ctx, provider, listed)
	if err != nil {
		failures = append(failures, fmt.Errorf("default selection: %w", err))
	}
	if defaults, err = excludeManaged(ctx, run, options.ManagedState, listed, defaults, importIDFunc(provider)); err != nil {
		return err
	}
	scope := discoveryScope(ctx, provider)
	if options.Discover {
		run.used = true
		run.options = options
		run.failures = append(run.failures, failures...)
		run.addDiscovered(listed, defaults, scope, importIDFunc(provider))
		return nil
	}
	chosen, err := run.selectionFile(options.Selection)
	if err != nil {
		return err
	}
	selected, leftOut := run.selectResources(listed, defaults, chosen, scope, importIDFunc(provider))
	if options.PathPattern, err = rootPathPattern(ctx, provider, options.PathPattern); err != nil {
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
	root, err := rootConfig(ctx, http.DefaultClient, binary.Registry, "", execPath, engineProvider(provider))
	if err != nil {
		return err
	}
	if err := writeGitignore(options.PathOutput); err != nil {
		return err
	}

	// Children follow their parent: only selected resources have them.
	resourcesByService, childFailures := withChildImports(ctx, provider, selected)
	failures = append(failures, childFailures...)
	opts := engineOptions(provider, root)
	opts.External = leftOut
	switch options.Modules {
	case "", modulesRegistry:
		opts.Adapters = adapters.For(provider.GetName())
		opts.ModulesDir = filepath.Join(options.PathOutput, engine.ModulesDirName)
	case modulesLocal:
		opts.ModulesDir = filepath.Join(options.PathOutput, engine.ModulesDirName)
	}
	byDir, skipped := importsByDir(provider.GetName(), options, resourcesByService, importIDFunc(provider))

	run.used = true
	run.options = options
	run.report.Manifest = report.Manifest{
		Tool:     report.Component{Name: "infraharvest", Version: version},
		Engine:   report.Component{Name: binary.Name, Version: root.engineVersion},
		Provider: report.Provider{Source: qualifiedSource(binary.Registry, root.provider.Source), Constraint: root.provider.Version},
	}
	for _, f := range failures {
		run.report.Failures = append(run.report.Failures, f.Error())
	}
	run.failures = append(run.failures, failures...)
	for typ, n := range skipped {
		run.skipped[typ] += n
	}
	for typ, n := range discoveredByType(resourcesByService) {
		run.discovered[typ] += n
	}

	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		fp, err := fingerprint(root.engineVersion, byDir[dir], opts)
		if err != nil {
			return err
		}
		var result *engine.Result
		if options.Resume {
			if result, err = resumed(options.PathOutput, dir, fp); err != nil {
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
			log.Printf("%s: %s is unchanged since it was generated (--resume)", provider.GetName(), dir)
			if run.lock == nil {
				run.lock, _ = os.ReadFile(filepath.Join(dir, engine.LockFileName))
			}
		case incremental:
			added, addedResult, holds, err := run.addToRoot(ctx, dir, byDir[dir], opts, execPath, filepath.Join(cacheDir, "plugins"))
			if err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil && holds != nil {
				if err := saveCheckpoint(options.PathOutput, dir, fp, holds); err != nil {
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
				if err := clearGenerated(dir); err != nil {
					return err
				}
			}
			log.Printf("%s: generating configuration for %d resources in %s", provider.GetName(), len(byDir[dir]), dir)
			result, err = generateDir(ctx, dir, execPath, filepath.Join(cacheDir, "plugins"), byDir[dir], opts, &run.lock)
			if err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil {
				if err := saveCheckpoint(options.PathOutput, dir, fp, result); err != nil {
					return err
				}
			}
		}
		run.addDirectory(dir, byDir[dir], result, err)
		if err == nil && run.backend != nil {
			// After the checks, which run on a local working directory: the
			// import never needs access to the state backend.
			if err := os.WriteFile(filepath.Join(dir, BackendFileName), run.backend.File(relativePath(options.PathOutput, dir)), 0o644); err != nil {
				return err
			}
		}
	}
	run.report.Provider.Version = engine.LockedVersion(run.lock, run.report.Provider.Source)
	return nil
}

// engineOptions configures engine.Generate for provider's directories.
func engineOptions(provider terraformutils.ProviderGenerator, root *rootFiles) engine.Options {
	opts := engine.Options{Config: root.files, Scanners: engine.DefaultScanners()}
	if withOmitted, ok := provider.(terraformutils.ProviderWithOmittedArguments); ok {
		opts.Omit = withOmitted.OmittedArguments()
	}
	if withStateOnly, ok := provider.(terraformutils.ProviderWithStateOnlyArguments); ok {
		opts.StateOnly = withStateOnly.StateOnlyArguments()
	}
	if withData, ok := provider.(terraformutils.ProviderWithDataSources); ok {
		opts.DataSources = map[string]engine.DataSource{}
		for typ, d := range withData.DataSources() {
			opts.DataSources[typ] = engine.DataSource{Type: d.Type, Argument: d.Argument}
		}
	}
	if withDefaultTags, ok := provider.(terraformutils.ProviderWithDefaultTags); ok {
		attribute, block, reserved := withDefaultTags.DefaultTags()
		opts.DefaultTags = &engine.DefaultTags{Provider: provider.GetName(), Attribute: attribute, Block: block, ReservedPrefix: reserved}
	}
	return opts
}

// addDirectory records what Generate did in dir, or err if it failed.
func (r *engineRun) addDirectory(dir string, imports []engine.Import, result *engine.Result, err error) {
	reported := report.Directory{Path: relativePath(r.options.PathOutput, dir)}
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
func discoveredByType(resourcesByService map[string][]terraformutils.Resource) map[string]int {
	discovered := map[string]int{}
	for _, resources := range resourcesByService {
		for _, r := range resources {
			discovered[r.InstanceInfo.Type]++
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

// generateDir runs engine.Generate in dir, seeding it with *lock if set and
// keeping its lock file in *lock otherwise.
func generateDir(ctx context.Context, dir, execPath, pluginCacheDir string, imports []engine.Import, opts engine.Options, lock *[]byte) (*engine.Result, error) {
	tf, err := engine.NewTerraform(dir, execPath, pluginCacheDir)
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
}

// rootConfig renders versions.tf and providers.tf for every output
// directory: required_version within the major release of the Terraform or
// OpenTofu at execPath, and the provider pinned to its newest minor release
// line in registry (the engine's default registry), unless its source names
// another. A registryURL replaces the registry, for tests.
func rootConfig(ctx context.Context, client *http.Client, registry, registryURL, execPath string, p engine.Provider) (*rootFiles, error) {
	engineVersion, err := engine.BinaryVersion(ctx, execPath)
	if err != nil {
		return nil, err
	}
	latest, err := engine.LatestProviderVersion(ctx, client, registryURL, qualifiedSource(registry, p.Source))
	if err != nil {
		return nil, err
	}
	p.Version = engine.ProviderConstraint(latest)
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
	return os.WriteFile(path, []byte(engine.GitignoreFile), 0o644)
}

// checkImportOptions rejects values of --output and --modules the import
// would otherwise silently ignore.
func checkImportOptions(options ImportOptions) error {
	if options.Output != "" && options.Output != outputHCL && options.Output != outputJSON {
		return fmt.Errorf("--output must be %s or %s, not %q", outputHCL, outputJSON, options.Output)
	}
	switch options.Modules {
	case "", modulesRegistry, modulesLocal, modulesNone:
	default:
		return fmt.Errorf("--modules must be %s, %s or %s, not %q", modulesRegistry, modulesLocal, modulesNone, options.Modules)
	}
	return checkFilters(options.Filter)
}

// checkFilters rejects a --filter that doesn't parse. Filters on IDs apply
// to every service; a filter on another attribute only takes effect where
// the service's lister passes it to the API it lists with (such as AWS EC2
// instance tags), so it says so.
func checkFilters(rawFilters []string) error {
	var s terraformutils.Service
	for _, raw := range rawFilters {
		filters := s.ParseFilter(raw)
		if len(filters) == 0 {
			return fmt.Errorf("--filter %q isn't a filter: use <service>=<id1>:<id2>, or [Type=<service>;]Name=<attribute>[;Value=<value1>:<value2>]", raw)
		}
		for _, f := range filters {
			if f.FieldPath != "id" {
				log.Printf("--filter %q: a filter on %s only applies to services whose lister supports it (see the provider's page); others list everything", raw, f.FieldPath)
			}
		}
	}
	return nil
}

// importsByDir groups resources into import blocks per output directory,
// following --path-pattern. It leaves out, logs and counts by type
// resources Terraform can't import.
func importsByDir(providerName string, options ImportOptions, resourcesByService map[string][]terraformutils.Resource, importID func(terraformutils.Resource) (string, bool)) (map[string][]engine.Import, map[string]int) {
	byDir := map[string][]engine.Import{}
	skipped := map[string]int{}
	for service, resources := range resourcesByService {
		dir := filepath.Clean(Path(options.PathPattern, providerName, service, options.PathOutput))
		for _, r := range resources {
			id, ok := importID(r)
			if !ok {
				skipped[r.InstanceInfo.Type]++
				continue
			}
			byDir[dir] = append(byDir[dir], engine.Import{
				Type:     r.InstanceInfo.Type,
				Name:     r.RawName,
				ID:       id,
				Provider: importProvider(providerName, r.InstanceInfo.Type),
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

// importIDFunc returns how to get a resource's import ID: the provider's
// mapping if it has one, else the ID its lister recorded.
func importIDFunc(provider terraformutils.ProviderGenerator) func(terraformutils.Resource) (string, bool) {
	if withIDs, ok := provider.(terraformutils.ProviderWithImportIDs); ok {
		return withIDs.ImportID
	}
	return func(r terraformutils.Resource) (string, bool) { return r.InstanceState.ID, true }
}

// engineProvider describes the provider for the generated configuration.
func engineProvider(provider terraformutils.ProviderGenerator) engine.Provider {
	name := provider.GetName()
	source := "hashicorp/" + name
	if withSource, ok := provider.(terraformutils.ProviderWithSource); ok {
		source = withSource.GetSource()
	}
	var config map[string]interface{}
	if providers, ok := provider.GetProviderData()["provider"].(map[string]interface{}); ok {
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
// the provider lists for them (see terraformutils.ProviderWithChildImports),
// and returns the resources it couldn't list children for as failures.
func withChildImports(ctx context.Context, provider terraformutils.ProviderGenerator, resourcesByService map[string][]terraformutils.Resource) (map[string][]terraformutils.Resource, []error) {
	withChildren, ok := provider.(terraformutils.ProviderWithChildImports)
	if !ok {
		return resourcesByService, nil
	}
	var failures []error
	all := make(map[string][]terraformutils.Resource, len(resourcesByService))
	for service, resources := range resourcesByService {
		all[service] = append([]terraformutils.Resource(nil), resources...)
		for _, r := range resources {
			children, err := withChildren.ChildImports(ctx, r)
			if err != nil {
				failures = append(failures, fmt.Errorf("%s: %s %s: %w", service, r.InstanceInfo.Type, r.InstanceState.ID, err))
			}
			all[service] = append(all[service], children...)
		}
	}
	return all, failures
}

// BackendFileName holds a generated root's state backend.
const BackendFileName = "backend.tf"

// DefaultRootPathPattern lays output out as one root per state boundary:
// account (or subscription or project), then region or global.
const DefaultRootPathPattern = "{output}/{provider}/{account}/{region}/"

// rootPathPattern returns the path pattern for the roots: pattern, or
// DefaultRootPathPattern if empty, with {account} and {region} filled in
// from the provider.
func rootPathPattern(ctx context.Context, provider terraformutils.ProviderGenerator, pattern string) (string, error) {
	if pattern == "" {
		pattern = DefaultRootPathPattern
	}
	if !strings.Contains(pattern, "{account}") && !strings.Contains(pattern, "{region}") {
		return pattern, nil
	}
	account, region := "default", "default"
	if withScope, ok := provider.(terraformutils.ProviderWithScope); ok {
		var err error
		if account, region, err = withScope.Scope(ctx); err != nil {
			return "", err
		}
	}
	return strings.NewReplacer("{account}", account, "{region}", region).Replace(pattern), nil
}
