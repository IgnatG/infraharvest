// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"maps"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// scopeRoots returns the roots the Import calls of one scope generate,
// the imports of each call's roots by directory (see listedCall), as call
// own sees them: its own roots with those of every other call, so that
// each refers to the others (see withOtherRoots), the regional roots of an
// AWS account to its global root and the global root to the regional ones.
// A directory of call own is its own, whatever another call puts there.
func scopeRoots(byDirs []map[string][]engine.Import, own int) map[string][]engine.Import {
	if len(byDirs) < 2 {
		if own < len(byDirs) {
			return byDirs[own]
		}
		return nil
	}
	all := map[string][]engine.Import{}
	for i, byDir := range byDirs {
		if i != own {
			maps.Copy(all, byDir)
		}
	}
	maps.Copy(all, byDirs[own])
	return all
}
