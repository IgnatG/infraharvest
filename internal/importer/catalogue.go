// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"maps"
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/internal/catalogue"
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
