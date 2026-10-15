// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"strings"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// providerCatalogue returns what the catalogue knows about the provider's
// resource types: the built-in entries, with the files in dir (from
// --catalogue) on top.
func providerCatalogue(provider harvest.Provider, dir string) (*catalogue.Catalogue, error) {
	return catalogue.Load(catalogueName(provider.Name()), dir)
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
