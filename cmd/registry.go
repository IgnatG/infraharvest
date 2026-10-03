// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"sort"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/spf13/cobra"
)

// providerEntry ties a provider's CLI command to its generator.
type providerEntry struct {
	newCmd      func(options ImportOptions) *cobra.Command
	newProvider func() terraformutils.ProviderGenerator
}

// providerRegistry holds the providers compiled into this binary. Each
// provider_cmd_<name>.go registers itself from init() and carries the build
// constraint `!minimal || <name>`: a plain build includes every provider,
// and `go build -tags minimal,aws,google` includes only those listed.
var providerRegistry = map[string]providerEntry{}

func registerProvider(name string, newCmd func(ImportOptions) *cobra.Command, newProvider func() terraformutils.ProviderGenerator) {
	if _, dup := providerRegistry[name]; dup {
		panic("provider registered twice: " + name)
	}
	providerRegistry[name] = providerEntry{newCmd: newCmd, newProvider: newProvider}
}

// registeredProviders returns the registered provider names, sorted.
func registeredProviders() []string {
	names := make([]string, 0, len(providerRegistry))
	for name := range providerRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
