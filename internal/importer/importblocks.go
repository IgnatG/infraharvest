// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"cmp"
	"log"
	"slices"
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// inOtherRoots returns, by type and import ID, the root of roots other
// than dir that each of their resources goes in, by its path relative to
// out.
func inOtherRoots(out string, roots map[string][]engine.Import, dir string) map[engine.External]string {
	other := map[engine.External]string{}
	for d, imports := range roots {
		if d == dir {
			continue
		}
		for _, imp := range imports {
			other[engine.ResourceKey(imp.Type, imp.ID)] = relativePath(out, d)
		}
	}
	return other
}

// importBlocks says how importsByDir writes the import block of a
// resource.
type importBlocks struct {
	// provider is the Terraform provider the roots declare, and configured
	// the other providers they may configure (see
	// engine.Options.ModuleProviders).
	provider   string
	configured map[string]engine.Provider
	// cat says what the configuration of a resource leaves out depending
	// on what its lister recorded, and the ID to import it by should
	// Terraform reject its own; nil says neither.
	cat      *catalogue.Catalogue
	importID func(harvest.Resource) (string, bool)
	// nameOf names a resource's block, and fileOf the file of its root
	// that it goes in.
	nameOf func(harvest.Resource) string
	fileOf func(service string, r harvest.Resource) string
}

// importsByDir groups resources into import blocks per output directory,
// the root dirOf names for each, each block whole, as blocks says (see
// importBlocks.of). It leaves out, logs and counts by type resources
// Terraform can't import.
func importsByDir(blocks importBlocks, resourcesByService map[string][]harvest.Resource, dirOf func(service string, r harvest.Resource) string) (map[string][]engine.Import, map[string]int) {
	byDir := map[string][]engine.Import{}
	skipped := map[string]int{}
	for service, resources := range resourcesByService {
		for _, r := range resources {
			dir := dirOf(service, r)
			imp, ok := blocks.of(service, r)
			if !ok {
				skipped[r.Address.Type]++
				continue
			}
			byDir[dir] = append(byDir[dir], imp)
		}
	}
	types := make([]string, 0, len(skipped))
	for typ := range skipped {
		types = append(types, typ)
	}
	sort.Strings(types)
	for _, typ := range types {
		log.Printf("%s: skipping %d %s: Terraform can't import this resource type", blocks.provider, skipped[typ], typ)
	}
	return byDir, skipped
}

// of returns the import block of r, a resource service listed, or false
// if Terraform can't import it. The block names the provider unless its
// type implies it (see importProvider), or one of the other providers
// the root may configure, such as azapi for an azapi_resource: the root
// requires that provider under its own name when it imports such a
// resource, so Terraform infers it from the type. It leaves out the
// arguments the catalogue leaves out depending on what r's lister
// recorded (see catalogue.Catalogue.ResourceOmit), and falls back on the
// catalogue's next import ID template for r, where that is the same
// resource by another query alone, as an azapi_resource's ID without
// ?api-version= is (see engine.Import.Fallback and engine.ResourceKey).
// A resource Terraform's list resources returned is imported by its
// identity instead (see engine.Import.Identity).
func (b importBlocks) of(service string, r harvest.Resource) (engine.Import, bool) {
	typ := r.Address.Type
	id, ok := b.importID(r)
	if !ok {
		return engine.Import{}, false
	}
	imp := engine.Import{Type: typ, Name: b.nameOf(r), File: b.fileOf(service, r), ID: id, Provider: importProvider(b.provider, typ)}
	implied, _, _ := strings.Cut(typ, "_")
	// google --beta's roots configure google too, for modules: its own
	// google_* resources still name google-beta.
	if _, ok := b.configured[implied]; ok && implied != catalogueName(b.provider) {
		imp.Provider = ""
	}
	// What Terraform's list resources returned is imported by its
	// identity: the catalogue makes no import ID of it.
	if r.Observed != nil && r.Observed.Identity != nil {
		imp.Identity = r.Observed.Identity
		return imp, true
	}
	if b.cat == nil || r.Observed == nil {
		return imp, true
	}
	if extra := b.cat.ResourceOmit(typ, r.Observed.Attributes); len(extra) > 0 {
		imp.Omit = extra
	}
	if fallback, ok := b.cat.FallbackImportID(typ, r.Observed.ID, r.Observed.Attributes); ok && engine.ResourceKey(typ, fallback) == engine.ResourceKey(typ, id) {
		imp.Fallback = fallback
	}
	return imp, true
}

// otherRoots is what the roots of an import manage, sorted once for the
// options of each of them (see of): with a {service} or {env} layout, a
// call has tens of roots, and each would otherwise sort what all the
// others manage again.
type otherRoots struct {
	opts engine.Options
	// several is whether there is more than one root, without which there
	// is no other root.
	several bool
	// external and resources are opts' own and what every root manages,
	// each with the root's directory ("" for opts' own), in the order of
	// the options of a root.
	external  []ofRoot[engine.External]
	resources []ofRoot[engine.RootResource]
}

// ofRoot is a value, of the root in dir.
type ofRoot[T any] struct {
	value T
	dir   string
}

// newOtherRoots indexes the roots of byDir, under the output directory
// out, for the options of each, which start from opts.
func newOtherRoots(opts engine.Options, byDir map[string][]engine.Import, out string) otherRoots {
	o := otherRoots{opts: opts, several: len(byDir) >= 2}
	if !o.several {
		return o
	}
	for _, e := range opts.External {
		o.external = append(o.external, ofRoot[engine.External]{value: e})
	}
	for _, r := range opts.OtherRoots {
		o.resources = append(o.resources, ofRoot[engine.RootResource]{value: r})
	}
	for dir, imports := range byDir {
		root := relativePath(out, dir)
		for _, imp := range imports {
			// External resources are only of use to the data sources that
			// read them (see engine.Options.External): the others would
			// only make the root's fingerprint change with resources it
			// can't refer to.
			if _, ok := opts.DataSources[imp.Type]; ok {
				o.external = append(o.external, ofRoot[engine.External]{value: engine.External{Type: imp.Type, ID: imp.ID, Name: imp.Name}, dir: dir})
			}
			o.resources = append(o.resources, ofRoot[engine.RootResource]{value: engine.RootResource{Type: imp.Type, ID: imp.ID, Name: engine.Label(imp.Name), Root: root}, dir: dir})
		}
	}
	// A name before none: the data source that reads it is named by it.
	unnamed := func(e engine.External) int {
		if e.Name == "" {
			return 1
		}
		return 0
	}
	slices.SortFunc(o.external, func(x, y ofRoot[engine.External]) int {
		a, b := x.value, y.value
		return cmp.Or(strings.Compare(a.Type, b.Type), strings.Compare(a.ID, b.ID), cmp.Compare(unnamed(a), unnamed(b)), strings.Compare(a.Name, b.Name))
	})
	slices.SortFunc(o.resources, func(x, y ofRoot[engine.RootResource]) int {
		a, b := x.value, y.value
		return cmp.Or(strings.Compare(a.Type, b.Type), strings.Compare(a.ID, b.ID), strings.Compare(a.Root, b.Root), strings.Compare(a.Name, b.Name))
	})
	return o
}

// of returns the options for the root in dir, with what the other roots
// manage as external: references to it read it, as references between
// roots do, or a variable says where it comes from.
func (o otherRoots) of(dir string) engine.Options {
	if !o.several {
		return o.opts
	}
	dirOpts := o.opts
	dirOpts.External, dirOpts.OtherRoots = nil, nil
	for _, e := range o.external {
		// Each resource once, by its first entry.
		if (e.dir != "" && e.dir == dir) || (len(dirOpts.External) > 0 && sameResource(dirOpts.External[len(dirOpts.External)-1], e.value)) {
			continue
		}
		dirOpts.External = append(dirOpts.External, e.value)
	}
	for _, r := range o.resources {
		if (r.dir != "" && r.dir == dir) || (len(dirOpts.OtherRoots) > 0 && dirOpts.OtherRoots[len(dirOpts.OtherRoots)-1] == r.value) {
			continue
		}
		dirOpts.OtherRoots = append(dirOpts.OtherRoots, r.value)
	}
	return dirOpts
}

// sameResource reports whether a and b are the same resource, whatever
// they are named.
func sameResource(a, b engine.External) bool {
	return engine.ResourceKey(a.Type, a.ID) == engine.ResourceKey(b.Type, b.ID)
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
