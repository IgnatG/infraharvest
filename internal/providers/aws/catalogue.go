// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// awsCatalogue is what the built-in catalogue knows about AWS types.
func awsCatalogue() *catalogue.Catalogue {
	return catalogue.MustBuiltin("aws")
}

// importID returns the ID Terraform imports r with, as the catalogue
// builds it, and false if Terraform can't import it.
func importID(r harvest.Resource) (string, bool) {
	id, ok, _ := awsCatalogue().ImportID(r.Address.Type, r.Observed.ID, r.Observed.Attributes)
	return id, ok
}
