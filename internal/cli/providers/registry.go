// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package providers holds the command of each provider infraharvest imports
// from: its flags, and how they become calls of the importer. Each
// provider_cmd_<name>.go describes its command with a definition (see
// command.go), registers it from init() and carries the build
// constraint `!minimal || <name>`: a plain build includes every provider,
// and `go build -tags minimal,aws,google` includes only those listed.
package providers

import (
	"sort"

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
