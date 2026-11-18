// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// keptOwned returns, as "<type> <import ID>", the selected resources whose
// listed tags say another tool manages them, as owners say: the
// selection file or --include-owned kept them, so the engine keeps them too (see
// engine.Options.KeepOwned).
func keptOwned(selected map[string][]harvest.Resource, listed listing, importID func(harvest.Resource) (string, bool), owners engine.OwnerTags) map[string]bool {
	var kept map[string]bool
	for _, resources := range selected {
		for _, res := range resources {
			if _, owned := owners.OwnedBy(listed.tagsOf(res)); !owned {
				continue
			}
			if id, ok := importID(res); ok {
				if kept == nil {
					kept = map[string]bool{}
				}
				kept[res.Address.Type+" "+id] = true
			}
		}
	}
	return kept
}

// ownedByTags is why an import leaves out a resource whose tags say o
// manages it, as the selection says it (see selection.OwnedReason).
func ownedByTags(o engine.Owner) report.Why {
	return report.Owned(selection.OwnedReason(o), selection.OwnedBy(o))
}

// listedByImportID returns, as "<type> <import ID>", the selected resources
// whose import ID is the ID they were listed by: the cloud reads them by
// the ID its listing gave, so an import that finds no object there finds
// a resource deleted since (see engine.Options.MayBeGone).
func listedByImportID(selected map[string][]harvest.Resource, importID func(harvest.Resource) (string, bool)) map[string]bool {
	listed := map[string]bool{}
	for _, resources := range selected {
		for _, res := range resources {
			if id, ok := importID(res); ok && res.Observed.Identity == nil && strings.EqualFold(id, res.Observed.ID) {
				listed[res.Address.Type+" "+id] = true
			}
		}
	}
	return listed
}
