// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"

	goversion "github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// moduleConstraint is what a module version the import may call accepts
// of the root's provider.
type moduleConstraint struct {
	module, version string
	constraint      goversion.Constraints
}

// resolveAdapters looks up the releases of the adapters' modules with
// releasesOf (see Run.moduleReleases). Each adapter calls
// the version it is tested with; with untested, the newest release
// instead. It returns the adapters to use, the modules with a newer
// release than the one used, and what the versions used accept of the
// provider at providerSource. An adapter whose module can't be looked up
// keeps its tested version.
func resolveAdapters(ctx context.Context, releasesOf func(ctx context.Context, source string) ([]engine.ModuleRelease, error), list []adapters.Adapter, providerSource string, untested bool) ([]adapters.Adapter, []report.ModuleVersion, []moduleConstraint) {
	used := make([]adapters.Adapter, 0, len(list))
	var newer []report.ModuleVersion
	var constraints []moduleConstraint
	for _, a := range list {
		releases, err := releasesOf(ctx, a.Source)
		if err != nil {
			log.Printf("%s: %v; calling the tested version %s", a.Source, err, a.Version)
			used = append(used, a)
			continue
		}
		latest := releases[0]
		if tested, err := goversion.NewVersion(a.Version); err == nil && latest.Version.GreaterThan(tested) {
			if untested {
				log.Printf("%s: calling %s, newer than the tested %s (--modules %s)", a.Source, latest.Version.Original(), a.Version, ModulesLatestUntested)
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

// moduleReleases are the releases of the registry modules a command
// looks up, by module source, each looked up once and shared by every
// Import call of the command: the query service resolves the roots' pin
// from the same modules the toolchain resolves again (see Run.callRoot),
// and the calls of --parallel run at once. The command looks them up in
// one registry, so the source alone tells them apart.
type moduleReleases struct {
	mu sync.Mutex
	of map[string]*moduleReleaseLookup
}

// moduleReleaseLookup is the lookup of one module's releases, which the
// calls that want them wait for.
type moduleReleaseLookup struct {
	mu       sync.Mutex
	done     bool
	releases []engine.ModuleRelease
	err      error
}

// get returns the releases of the module at source, looking them up with
// list on first use. A lookup cut short by its own context isn't kept, so
// that a later call, with a context still live, looks again.
func (m *moduleReleases) get(ctx context.Context, source string, list func(context.Context) ([]engine.ModuleRelease, error)) ([]engine.ModuleRelease, error) {
	m.mu.Lock()
	if m.of == nil {
		m.of = map[string]*moduleReleaseLookup{}
	}
	l, ok := m.of[source]
	if !ok {
		l = &moduleReleaseLookup{}
		m.of[source] = l
	}
	m.mu.Unlock()

	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.done {
		releases, err := list(ctx)
		if err != nil && ctx.Err() != nil {
			return nil, err
		}
		l.releases, l.err, l.done = releases, err, true
	}
	return l.releases, l.err
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

// newestMajor splits constraints into the module versions that accept a
// release of the provider at source in latest's major release line, and
// those that accept only older majors. A module that would hold the whole
// root a major release behind isn't called: its resources stay in the
// root, as plain resources of the newest provider, which is what someone
// writing the configuration today would use.
func newestMajor(ctx context.Context, client *http.Client, registryURL, source string, latest *goversion.Version, constraints []moduleConstraint) (admitted, refused []moduleConstraint) {
	for _, c := range constraints {
		v, err := engine.ProviderVersion(ctx, client, registryURL, source, []goversion.Constraints{c.constraint})
		if err != nil || v.Segments()[0] != latest.Segments()[0] {
			refused = append(refused, c)
			continue
		}
		admitted = append(admitted, c)
	}
	return admitted, refused
}

// withoutModules returns list without the adapters of the module versions
// in refused.
func withoutModules(list []adapters.Adapter, refused []moduleConstraint) []adapters.Adapter {
	if len(refused) == 0 {
		return list
	}
	kept := make([]adapters.Adapter, 0, len(list))
	for _, a := range list {
		if !slices.ContainsFunc(refused, func(c moduleConstraint) bool { return c.module == a.Source && c.version == a.Version }) {
			kept = append(kept, a)
		}
	}
	return kept
}

// applicableAdapters returns the adapters whose anchor type the selected
// resources have: only they can map a cluster, so only their modules'
// constraints should hold the provider back, and only their modules need
// looking up.
func applicableAdapters(list []adapters.Adapter, selected map[string][]harvest.Resource) []adapters.Adapter {
	types := map[string]bool{}
	for _, resources := range selected {
		for _, r := range resources {
			types[r.Address.Type] = true
		}
	}
	var applicable []adapters.Adapter
	for _, a := range list {
		if types[a.Anchor] {
			applicable = append(applicable, a)
		}
	}
	return applicable
}
