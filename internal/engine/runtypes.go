// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import "github.com/IgnatG/infraharvest/internal/engine/run"

// The options and result of a run, and the types they are made of, are
// declared in package run, which the engine's sub-packages share. They stay
// the engine's names for its callers.
type (
	// Options configure Generate.
	Options = run.Options
	// Result is what Generate leaves for the user to finish.
	Result = run.Result

	Managed       = run.Managed
	Rejection     = run.Rejection
	OwnedResource = run.OwnedResource
	ModuleCall    = run.ModuleCall

	External      = run.External
	DataSource    = run.DataSource
	RootResource  = run.RootResource
	ARN           = run.ARN
	Names         = run.Names
	ScopeLocals   = run.ScopeLocals
	ScopeValue    = run.ScopeValue
	DefaultTags   = run.DefaultTags
	EffectiveTags = run.EffectiveTags

	Placeholder = run.Placeholder
	Lifecycle   = run.Lifecycle
	ImportOnce  = run.ImportOnce
	Previous    = run.Previous
)
