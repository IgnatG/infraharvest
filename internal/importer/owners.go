// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// resourceTypeOf returns the type of a resource address.
func resourceTypeOf(address string) string {
	t, _, _ := strings.Cut(address, ".")
	return t
}

// keptOwned returns, as "<type> <import ID>", the selected resources whose
// listed tags say another tool manages them: the selection file or
// --include-owned kept them, so the engine keeps them too (see
// engine.Options.KeepOwned).
func keptOwned(selected map[string][]harvest.Resource, listed listing, importID func(harvest.Resource) (string, bool)) map[string]bool {
	var kept map[string]bool
	for _, resources := range selected {
		for _, res := range resources {
			if _, owned := engine.OwnedBy(listed.tagsOf(res)); !owned {
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
