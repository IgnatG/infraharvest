// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import "github.com/IgnatG/infraharvest/internal/engine/run"

// What the provider's resources and the values that identify them are like,
// and which tool a tag says manages a resource (see run.Traits).
type (
	Traits    = run.Traits
	OwnerTags = run.OwnerTags
	Owner     = run.Owner
	OwnerTag  = run.OwnerTag
)
