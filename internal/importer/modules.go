// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"path/filepath"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine/hclfile"
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
		return resolved, filepath.Join(out, hclfile.ModulesDirName)
	case mode == ModulesLocal:
		return nil, filepath.Join(out, hclfile.ModulesDirName)
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
	if options.stackScoped() && !options.ModulesChosen {
		return ModulesNone
	}
	return options.Modules
}
