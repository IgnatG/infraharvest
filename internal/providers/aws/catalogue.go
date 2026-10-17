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

// SetCatalogue takes the catalogue of the import, the built-in one with
// the --catalogue overlay merged in, for what the provider reads from the
// catalogue once services are listed: import IDs, ARNs and children (see
// catalogue). The Cloud Control services, which are known before the
// overlay is loaded, come from the built-in catalogue.
func (p *Provider) SetCatalogue(c *catalogue.Catalogue) {
	p.cat = c
}

// catalogue returns the catalogue of the import (see SetCatalogue), or the
// built-in one before it is set.
func (p *Provider) catalogue() *catalogue.Catalogue {
	if p.cat != nil {
		return p.cat
	}
	return awsCatalogue()
}

// importID returns the ID Terraform imports r with, as cat builds it, and
// false if Terraform can't import it.
func importID(cat *catalogue.Catalogue, r harvest.Resource) (string, bool) {
	id, ok, _ := cat.ImportID(r.Address.Type, r.Observed.ID, r.Observed.Attributes)
	return id, ok
}
