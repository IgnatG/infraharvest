// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package providers holds the command of each provider infraharvest imports
// from: its flags, and how they become calls of the importer. Each
// provider_cmd_<name>.go describes its command with a definition (see
// command.go), registers it from init() and carries the build
// constraint `!minimal || <name>`: a plain build includes every provider,
// and `go build -tags minimal,aws,google` includes only those listed.
// A command takes flags only, and checks them before it plans anything;
// --check stops there.
package providers

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/importer"
)

// entry ties a provider's command to its constructor.
type entry struct {
	newCmd      func(options importer.Options) (*cobra.Command, RunFunc)
	newProvider func() harvest.Provider
}

// registry holds the providers compiled into this binary, by the name of
// their command.
var registry = map[string]entry{}

func register(name string, newCmd func(importer.Options) (*cobra.Command, RunFunc), newProvider func() harvest.Provider) {
	if _, dup := registry[name]; dup {
		panic("provider registered twice: " + name)
	}
	registry[name] = entry{newCmd: newCmd, newProvider: newProvider}
}

// Names returns the names of the providers compiled in, sorted.
func Names() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Profiles returns the profiles of the providers compiled in, by the
// names of their commands, sorted: the configuration file reads what their
// accounts, subscriptions and projects are like from them (see
// config.Load).
func Profiles() []harvest.Profile {
	profiles := make([]harvest.Profile, 0, len(registry))
	for _, name := range Names() {
		profiles = append(profiles, registry[name].newProvider().Profile())
	}
	return profiles
}

// Sources returns, by the names of their commands, the source addresses
// of the Terraform providers the roots of each provider compiled in use:
// its own, such as hashicorp/azurerm, then those its curated modules use,
// such as Azure/azapi (see harvest.ProviderWithModuleProviders), sorted.
// The MCP server's drift tools run Terraform on roots that use these
// alone (see mcpserver.Providers).
func Sources() map[string][]string {
	sources := make(map[string][]string, len(registry))
	for name, e := range registry {
		p := e.newProvider()
		own := p.Profile().Source
		if own == "" {
			own = "hashicorp/" + p.Name()
		}
		var modules []string
		if m, ok := p.(harvest.ProviderWithModuleProviders); ok {
			for _, mp := range m.ModuleProviders() {
				modules = append(modules, mp.Source)
			}
		}
		slices.Sort(modules)
		sources[name] = append([]string{own}, slices.Compact(modules)...)
	}
	return sources
}

// Check rejects name unless it names a provider compiled in. The services
// command and the MCP server's tools, which put the name on a command line
// of their own, check it here, so that both say the same.
func Check(name string) error {
	if _, ok := registry[name]; !ok {
		return fmt.Errorf("unknown provider %q: this infraharvest has %s", name, strings.Join(Names(), ", "))
	}
	return nil
}

// Command returns the named provider's command, which import and discover
// add as a subcommand, with the options its flags fill in, and what runs
// it.
func Command(name string, options importer.Options) (*cobra.Command, RunFunc) {
	return registry[name].newCmd(options)
}

// New returns a new provider of the given name, or nil when it isn't
// compiled in.
func New(name string) harvest.Provider {
	e, ok := registry[name]
	if !ok {
		return nil
	}
	return e.newProvider()
}
