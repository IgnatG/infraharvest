// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"strings"

	"github.com/magodo/armid"
	"github.com/magodo/azlist/azlist"
	"github.com/magodo/aztft/aztft"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// azapiProviderType is the provider type (Init's args[2]) of --azapi,
// with which resource_graph imports what azurerm has no type for through
// the Azure/azapi provider (see azapiCandidates).
const azapiProviderType = "azapi"

// azapiType is the azapi provider's generic resource, which manages any
// Resource Manager resource by its type, API version and body.
const azapiType = "azapi_resource"

// azapiCandidates returns, by upper-case ID, the resources that come
// through azapi_resource with --azapi where azurerm has no type for them:
// those Resource Graph returned, rows. The child resources Resource
// Manager lists under them don't, as most of those that azurerm has no
// type for are settings every parent has, which Azure creates and deletes
// with it, such as a database's advisors or a site's built-in
// configuration: they are left out as without --azapi.
func azapiCandidates(rows []azlist.AzureResource) map[string]bool {
	candidates := make(map[string]bool, len(rows))
	for _, res := range rows {
		candidates[strings.ToUpper(res.Id.String())] = true
	}
	return candidates
}

// azapiMapping maps the resource id, which azurerm has no type for, to an
// azapi_resource, imported by the resource's ID with ?api-version= of the
// newest stable API version of its type, which withAPIVersions records and
// the azapi catalogue's import ID template appends. Without one, azapi
// imports the resource with the newest API version its embedded Azure
// schemas have, falling back to the two before it if Resource Manager
// refuses that one. Either way it writes the version into the
// configuration's type, as type@version, which pins it from then on.
// From its schemas of that version it writes only the body's writable
// properties, so the configuration has none Azure sets itself, such as
// provisioningState; a version its schemas don't have, such as one newer
// than the azapi release, gives a body with all Azure returns, whose
// location and tags clash with the arguments, so the plan fails, and the
// engine imports the resource again by its ID alone, the catalogue's next
// template (see rootfile.Import.Fallback). Resource Manager leaves secrets,
// such as passwords, out of what it returns, so the body has none, and
// azapi ignores the properties Azure doesn't return; a secret it does
// return the engine moves into a sensitive variable, as it does any
// resource's. A type azapi has no schemas for can't be imported so:
// Terraform's import fails, by either ID, and the engine leaves the
// resource out with that error (see engine.Rejection).
func azapiMapping(id armid.ResourceId) mapping {
	return mapping{types: []aztft.Type{{AzureId: id, TFType: azapiType}}, ids: []string{id.String()}}
}

// countType returns how many of resources are of type typ.
func countType(resources []harvest.Resource, typ string) int {
	n := 0
	for _, r := range resources {
		if r.Address != nil && r.Address.Type == typ {
			n++
		}
	}
	return n
}
