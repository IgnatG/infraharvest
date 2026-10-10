// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	goversion "github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/adapters"
	"github.com/IgnatG/infraharvest/engine"
	"github.com/IgnatG/infraharvest/report"
)

// moduleConstraint is what a module version the import may call accepts
// of the root's provider.
type moduleConstraint struct {
	module, version string
	constraint      goversion.Constraints
}

// resolveAdapters looks up the releases of the adapters' modules in the
// registry at registryURL ("" for the public registry). Each adapter calls
// the version it is tested with; with untested, the newest release
// instead. It returns the adapters to use, the modules with a newer
// release than the one used, and what the versions used accept of the
// provider at providerSource. An adapter whose module can't be looked up
// keeps its tested version.
func resolveAdapters(ctx context.Context, client *http.Client, registryURL string, list []adapters.Adapter, providerSource string, untested bool) ([]adapters.Adapter, []report.ModuleVersion, []moduleConstraint) {
	used := make([]adapters.Adapter, 0, len(list))
	var newer []report.ModuleVersion
	var constraints []moduleConstraint
	for _, a := range list {
		releases, err := engine.ModuleReleases(ctx, client, registryURL, a.Source)
		if err != nil {
			log.Printf("%s: %v; calling the tested version %s", a.Source, err, a.Version)
			used = append(used, a)
			continue
		}
		latest := releases[0]
		if tested, err := goversion.NewVersion(a.Version); err == nil && latest.Version.GreaterThan(tested) {
			if untested {
				log.Printf("%s: calling %s, newer than the tested %s (--modules %s)", a.Source, latest.Version.Original(), a.Version, modulesLatestUntested)
				a.Version = latest.Version.Original()
			} else {
				newer = append(newer, report.ModuleVersion{Source: a.Source, Version: a.Version, Latest: latest.Version.Original()})
			}
		}
		used = append(used, a)
		for _, r := range releases {
			if r.Version.Original() != a.Version && r.Version.String() != a.Version {
				continue
			}
			if c, ok := r.Providers[providerKey(providerSource)]; ok {
				constraints = append(constraints, moduleConstraint{module: a.Source, version: a.Version, constraint: c})
			}
		}
	}
	return used, newer, constraints
}

// providerKey is how engine.ModuleRelease keys a provider: namespace/type,
// in lower case.
func providerKey(source string) string {
	parts := strings.Split(strings.ToLower(source), "/")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, "/")
}

// heldBack says which of constraints keep the provider below latest.
func heldBack(latest *goversion.Version, constraints []moduleConstraint) string {
	var by []string
	for _, c := range constraints {
		if !c.constraint.Check(latest) {
			by = append(by, fmt.Sprintf("%s %s (%s)", c.module, c.version, c.constraint))
		}
	}
	if len(by) == 0 {
		return ""
	}
	verb := "accepts"
	if len(by) > 1 {
		verb = "accept"
	}
	return fmt.Sprintf("%s is available, but %s %s only older releases", latest, strings.Join(by, ", "), verb)
}
