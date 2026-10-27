// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"log"
	"maps"
	"slices"
	"strings"

	goversion "github.com/hashicorp/go-version"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/catalogue/schemameta"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// providerCatalogue returns what the catalogue knows about the provider's
// resource types: the built-in entries, with the files in dir (from
// --catalogue) on top, and those of the other providers its roots may
// configure (see harvest.ProviderWithModuleProviders) that have a
// catalogue of their own, such as azapi, whose azapi_resource an Azure
// import brings in for what azurerm has no type for (see
// catalogue.Catalogue.With).
func providerCatalogue(provider harvest.Provider, dir string) (*catalogue.Catalogue, error) {
	name := catalogueName(provider.Name())
	cat, err := catalogue.Load(name, dir)
	if err != nil {
		return nil, err
	}
	withModuleProviders, ok := provider.(harvest.ProviderWithModuleProviders)
	if !ok {
		return cat, nil
	}
	known, err := catalogue.Providers()
	if err != nil {
		return nil, err
	}
	for _, other := range slices.Sorted(maps.Keys(withModuleProviders.ModuleProviders())) {
		if other = catalogueName(other); other == name || !slices.Contains(known, other) {
			continue
		}
		companion, err := catalogue.Load(other, dir)
		if err != nil {
			return nil, err
		}
		cat = cat.With(companion)
	}
	return cat, nil
}

// catalogueName is the catalogue of a provider's types: google for
// google-beta, whose types are google_*.
func catalogueName(provider string) string {
	name, _, _ := strings.Cut(provider, "-")
	return name
}

// importIDFunc returns how to get a resource's import ID: from the
// catalogue's templates, else the ID its lister recorded. It returns
// false for resources Terraform can't import.
func importIDFunc(cat *catalogue.Catalogue) func(harvest.Resource) (string, bool) {
	return func(r harvest.Resource) (string, bool) {
		id, ok, _ := cat.ImportID(r.Address.Type, r.Observed.ID, r.Observed.Attributes)
		return id, ok
	}
}

// catalogueEngineOptions returns the engine's options of what cat knows
// about its provider's resource types, for roots that pin the provider
// release line of v: the arguments to leave out, their defaults and those
// kept only in state, the empty values that are settings of their own,
// the provider's built-in schema metadata, and the data sources, ARNs,
// placeholders and lifecycle settings of each type. The rest of the
// options are the caller's. The mapping lives here, not in the catalogue,
// so that the catalogue, which the engine's schema metadata is part of,
// never depends on the engine.
func catalogueEngineOptions(cat *catalogue.Catalogue, v *goversion.Version) engine.Options {
	opts := engine.Options{Omit: cat.Omit(v), Defaults: cat.Defaults(v), StateOnly: cat.StateOnly(v), KeepEmpty: cat.KeepEmpty()}
	// Without the metadata, the engine finds defaults by planning, as it
	// always could: a broken file costs plans, not the import.
	if meta, err := schemameta.Builtin(cat.Provider()); err != nil {
		log.Printf("%s: the built-in schema metadata: %v", cat.Provider(), err)
	} else {
		opts.SchemaMetadata = meta
	}
	if sources := cat.DataSources(); len(sources) > 0 {
		opts.DataSources = map[string]engine.DataSource{}
		for typ, d := range sources {
			opts.DataSources[typ] = engine.DataSource{Type: d.Type, Argument: d.Argument}
		}
	}
	if arns := cat.ARNs(); len(arns) > 0 {
		opts.ARNs = map[string]engine.ARN{}
		for typ, a := range arns {
			opts.ARNs[typ] = engine.ARN{Service: a.Service, ResourceType: a.ResourceType}
		}
	}
	if placeholders := cat.Placeholders(); len(placeholders) > 0 {
		opts.Placeholders = map[string]engine.Placeholder{}
		for typ, p := range placeholders {
			opts.Placeholders[typ] = engine.Placeholder{OneOf: p.OneOf, Set: p.Set, Ignore: p.Ignore, Reason: p.Reason}
		}
	}
	if lifecycles := cat.Lifecycles(); len(lifecycles) > 0 {
		opts.Lifecycles = map[string]engine.Lifecycle{}
		for typ, l := range lifecycles {
			opts.Lifecycles[typ] = engine.Lifecycle{CreateBeforeDestroy: l.CreateBeforeDestroy, Reason: l.Reason}
		}
	}
	return opts
}

// catalogueVerifyOptions returns the engine's options of what cat knows
// about its provider's resource types that the verification gate needs,
// for roots that pin the provider release line of v: the arguments to
// leave out, which the standards check doesn't hold against the root, and
// those kept only in state, whose updates the zero-change check allows.
// The rest of the options are the caller's.
func catalogueVerifyOptions(cat *catalogue.Catalogue, v *goversion.Version) engine.Options {
	return engine.Options{Omit: cat.Omit(v), StateOnly: cat.StateOnly(v)}
}

// catalogueDriftOptions returns the engine's drift options of what cat
// knows about its provider's resource types, for roots that pin the
// provider release line of v: the arguments kept only in state, whose
// changes aren't drift, their defaults, and the arguments to leave out.
// The rest of the options are the caller's.
func catalogueDriftOptions(cat *catalogue.Catalogue, v *goversion.Version) engine.DriftOptions {
	return engine.DriftOptions{StateOnly: cat.StateOnly(v), Defaults: cat.Defaults(v), Omit: cat.Omit(v)}
}
