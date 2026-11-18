// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import "github.com/IgnatG/infraharvest/internal/engine/run"

// ResourceKey is what a resource of type typ, imported by id, is told apart
// by from one run to the next (see run.ResourceKey).
func ResourceKey(typ, id string) External {
	return run.ResourceKey(typ, id)
}
