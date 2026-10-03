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
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/IgnatG/infraharvest/engine"
	"github.com/IgnatG/infraharvest/report"
	"github.com/IgnatG/infraharvest/terraformutils"
)

// Import engines selectable with --engine.
const (
	engineLegacy    = "legacy"
	engineTerraform = "terraform"
	engineTofu      = "tofu"
)

// Values of --output. With the legacy engine they choose the format of the
// generated files; with Terraform or OpenTofu, json prints the import report
// (see package report) on stdout.
const (
	outputHCL  = "hcl"
	outputJSON = "json"
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
// Nothing is refreshed through the embedded provider wrapper and no state
// is written.
func importWithEngine(provider terraformutils.ProviderGenerator, options ImportOptions, args []string) error {
	if err := checkTerraformEngineOptions(options); err != nil {
		return err
	}
	binary := engineBinary(options.Engine)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := provider.Init(args); err != nil {
		return err
	}
	options = resolveServices(provider, options)
	mapping := terraformutils.NewProvidersMapping(provider)
	failures, err := initAllServicesResources(mapping, options, args, nil)
	if err != nil {
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

	resourcesByService := mapping.GetResourcesByService()
	byDir, skipped := importsByDir(provider.GetName(), options, resourcesByService, importIDFunc(provider))
	rep := &report.Report{Manifest: report.Manifest{
		Tool:     report.Component{Name: "infraharvest", Version: version},
		Engine:   report.Component{Name: binary.Name, Version: root.engineVersion},
		Provider: report.Provider{Source: qualifiedSource(binary.Registry, root.provider.Source), Constraint: root.provider.Version},
	}}
	for _, f := range failures {
		rep.Failures = append(rep.Failures, f.Error())
	}
	for typ, n := range skipped {
		rep.Skipped = append(rep.Skipped, report.Skipped{Type: typ, Count: n, Reason: "Terraform can't import this resource type"})
	}

	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	// Every directory requires the same provider. Reusing the first lock
	// file pins one provider version for the whole import, and lets
	// Terraform install it from the plugin cache, which it only does for
	// providers a lock file records.
	var lock []byte
	failed := map[string]int{}
	for _, dir := range dirs {
		log.Printf("%s: generating configuration for %d resources in %s", provider.GetName(), len(byDir[dir]), dir)
		result, err := generateDir(ctx, dir, execPath, filepath.Join(cacheDir, "plugins"), root.files, byDir[dir], &lock)
		reported := report.Directory{Path: relativePath(options.PathOutput, dir)}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failures = append(failures, fmt.Errorf("%s: %w", dir, err))
			reported.Error = err.Error()
			for _, imp := range byDir[dir] {
				failed[imp.Type]++
			}
			rep.Directories = append(rep.Directories, reported)
			continue
		}
		reported.Imported = make([]report.Resource, 0, len(result.Imported))
		for _, imp := range result.Imported {
			reported.Imported = append(reported.Imported, report.Resource{Address: imp.Type + "." + imp.Name, ID: imp.ID})
		}
		for _, r := range result.Rejected {
			failures = append(failures, fmt.Errorf("%s: %s left out (see %s): %s", dir, r.Address, engine.RejectedFileName, strings.Join(r.Errors, "; ")))
			reported.LeftOut = append(reported.LeftOut, report.LeftOut{Address: r.Address, ID: r.ID, Errors: r.Errors})
		}
		for _, s := range result.Secrets {
			reported.Secrets = append(reported.Secrets, report.Secret{Variable: s.Variable, Address: s.Address, Attribute: s.Attribute})
		}
		if len(result.Secrets) > 0 {
			log.Printf("%s: set %d secret variables before planning (see %s)", dir, len(result.Secrets), engine.VariablesFileName)
		}
		rep.Directories = append(rep.Directories, reported)
	}

	rep.Provider.Version = engine.LockedVersion(lock, rep.Provider.Source)
	rep.Finish(discoveredByType(resourcesByService), failed, options.AllowPartial)
	if err := rep.WriteFiles(options.PathOutput); err != nil {
		return err
	}
	log.Printf("%s: imported %d of %d resources; report in %s", provider.GetName(), rep.Totals.Imported, rep.Totals.Discovered, filepath.Join(options.PathOutput, report.Dir, "report.md"))
	if options.Output == outputJSON {
		if err := rep.WriteJSON(os.Stdout); err != nil {
			return err
		}
	}
	return checkFailures(failures, options.AllowPartial)
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
func generateDir(ctx context.Context, dir, execPath, pluginCacheDir string, config map[string][]byte, imports []engine.Import, lock *[]byte) (*engine.Result, error) {
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
	result, err := engine.Generate(ctx, tf, dir, config, imports)
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

// checkTerraformEngineOptions rejects legacy options the Terraform engine
// would otherwise silently ignore.
func checkTerraformEngineOptions(options ImportOptions) error {
	var unsupported []string
	if options.Plan {
		unsupported = append(unsupported, "plan files")
	}
	if options.State != DefaultState {
		unsupported = append(unsupported, "--state "+options.State)
	}
	if options.Output != outputHCL && options.Output != outputJSON {
		unsupported = append(unsupported, "--output "+options.Output)
	}
	if options.Compact {
		unsupported = append(unsupported, "--compact")
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("--engine=%s does not support %s", options.Engine, strings.Join(unsupported, ", "))
	}
	return nil
}

// importsByDir groups resources into import blocks per output directory,
// following --path-pattern like the legacy engine. It leaves out, logs and
// counts by type resources Terraform can't import.
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
				Type: r.InstanceInfo.Type,
				Name: listedName(r.ResourceName),
				ID:   id,
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

var legacyEscape = regexp.MustCompile(`-([0-9A-F]{4})-`)

// listedName undoes the legacy sanitizing of a resource name ("tfer--"
// prefix, "-002F-" for "/"), so the engine labels the name as listed.
func listedName(sanitized string) string {
	name := strings.TrimPrefix(sanitized, "tfer--")
	return legacyEscape.ReplaceAllStringFunc(name, func(escaped string) string {
		code, err := strconv.ParseUint(escaped[1:5], 16, 32)
		if err != nil {
			return escaped
		}
		return string(rune(code))
	})
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
