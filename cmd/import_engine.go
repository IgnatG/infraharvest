// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/engine"
	"github.com/IgnatG/infraharvest/terraformutils"
)

// Import engines selectable with --engine.
const (
	engineLegacy    = "legacy"
	engineTerraform = "terraform"
)

// importWithTerraform lists resources with the provider's listers, then lets
// Terraform generate their configuration from import blocks. Nothing is
// refreshed through the embedded provider wrapper and no state is written.
func importWithTerraform(provider terraformutils.ProviderGenerator, options ImportOptions, args []string) error {
	if err := checkTerraformEngineOptions(options); err != nil {
		return err
	}
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
	execPath, err := engine.FindTerraform(ctx, options.TerraformPath, filepath.Join(cacheDir, "terraform"))
	if err != nil {
		return err
	}
	providerHCL, err := engine.ProvidersFile(engineProvider(provider))
	if err != nil {
		return err
	}

	byDir := importsByDir(provider.GetName(), options, mapping.GetResourcesByService(), importIDFunc(provider))
	var fixup engine.Fixup
	if withFixups, ok := provider.(terraformutils.ProviderWithConfigFixups); ok {
		fixup = withFixups.FixGeneratedConfig
	}
	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		log.Printf("%s: generating configuration for %d resources in %s", provider.GetName(), len(byDir[dir]), dir)
		tf, err := engine.NewTerraform(dir, execPath, filepath.Join(cacheDir, "plugins"))
		if err == nil {
			err = engine.Generate(ctx, tf, dir, providerHCL, byDir[dir], fixup)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failures = append(failures, fmt.Errorf("%s: %w", dir, err))
		}
	}
	return checkFailures(failures, options.AllowPartial)
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
	if options.Output != "hcl" {
		unsupported = append(unsupported, "--output "+options.Output)
	}
	if options.Compact {
		unsupported = append(unsupported, "--compact")
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("--engine=%s does not support %s", engineTerraform, strings.Join(unsupported, ", "))
	}
	return nil
}

// importsByDir groups resources into import blocks per output directory,
// following --path-pattern like the legacy engine. It leaves out, and logs,
// resources Terraform can't import.
func importsByDir(providerName string, options ImportOptions, resourcesByService map[string][]terraformutils.Resource, importID func(terraformutils.Resource) (string, bool)) map[string][]engine.Import {
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
				Name: r.ResourceName,
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
	return byDir
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
