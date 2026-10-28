// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package azure

import (
	"slices"
	"strings"

	"github.com/magodo/azlist/azlist"
	"github.com/magodo/aztft/aztft"

	"github.com/IgnatG/infraharvest/internal/catalogue"
)

// withoutUnkeptChildren stops azlist listing the child types whose
// resources the import would leave out, where none of their own child
// types has resources it keeps either. azlist lists every child type the
// ARM schema has under each resource it lists, one request per parent and
// type, and again under each child it finds: an App Service site has 20
// child types, an API Management service 34, most of them settings
// azurerm has no resource type for. On a large subscription those lists
// alone take longer than --list-timeout. keeps says which Resource
// Manager types the import keeps (see keptTypes). tree is the lister's own
// copy of the schema tree, built for it, keyed by upper-case type, with
// each entry's children by the last segment of their type.
func withoutUnkeptChildren(tree azlist.ARMSchemaTree, keeps func(armType string) bool) {
	kept := map[string]bool{}
	var keptBelow func(armType string, entry *azlist.ARMSchemaEntry) bool
	keptBelow = func(armType string, entry *azlist.ARMSchemaEntry) bool {
		if k, ok := kept[armType]; ok {
			return k
		}
		k := keeps(armType)
		for segment, child := range entry.Children {
			// Each child is walked, so every level is pruned.
			k = keptBelow(armType+"/"+segment, child) || k
		}
		kept[armType] = k
		return k
	}
	for armType, entry := range tree {
		for segment, child := range entry.Children {
			if !keptBelow(armType+"/"+segment, child) {
				delete(entry.Children, segment)
			}
		}
	}
}

// keptTypes reports whether the import keeps resources of a Resource
// Manager type: whether the catalogue's inventory entries or aztft map it
// to an azurerm type the import wants, under a resource group or a
// subscription. graphResources records nothing else of a child resource:
// azapi_resource only takes what Resource Graph returned (see
// azapiCandidates). The type is matched whatever its case.
func keptTypes(cat *catalogue.Catalogue, wants func(typ string) bool) func(armType string) bool {
	return func(armType string) bool {
		types := cat.InventoryTypes(armType)
		if strings.EqualFold(armType, workspaceTableType) {
			types = append(types, microsoftTableType, customLogTableType)
		}
		for _, id := range sampleIDs(armType) {
			mapped, _, err := aztft.QueryType(id, nil)
			if err != nil {
				continue
			}
			for _, t := range mapped {
				types = append(types, t.TFType)
			}
		}
		return slices.ContainsFunc(types, wants)
	}
}

// sampleIDs returns IDs of a resource of armType, such as
// Microsoft.Web/sites/slots, in a resource group and in a subscription,
// which aztft maps as it maps a listed resource of that type: by its
// type and the scope it is in.
func sampleIDs(armType string) []string {
	namespace, types, ok := strings.Cut(armType, "/")
	if !ok {
		return nil
	}
	path := "/providers/" + namespace
	for _, t := range strings.Split(types, "/") {
		path += "/" + t + "/x"
	}
	const subscription = "/subscriptions/00000000-0000-0000-0000-000000000000"
	return []string{subscription + "/resourceGroups/x" + path, subscription + path}
}
