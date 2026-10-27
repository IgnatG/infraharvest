// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// relativePath returns dir relative to the output directory, with forward
// slashes, so reports don't depend on where or on which OS they ran.
func relativePath(outputDir, dir string) string {
	rel, err := filepath.Rel(outputDir, dir)
	if err != nil {
		rel = dir
	}
	return filepath.ToSlash(rel)
}

// layoutDirs returns the root of each resource of a service, by layout
// (see rootLayouts) under out, with dirName for {provider} and envOf for
// {env}.
func layoutDirs(layout, out, dirName string, envOf func(harvest.Resource) string) func(service string, r harvest.Resource) string {
	return func(service string, r harvest.Resource) string {
		return rootDir(layout, rootValues{out: out, provider: dirName, service: service, env: envOf(r)})
	}
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
