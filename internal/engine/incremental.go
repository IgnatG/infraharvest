// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"

	"github.com/IgnatG/infraharvest/internal/engine/incremental"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// Add adds the resources of imports to root, a root an earlier import
// generated, without changing what root has (see incremental.Engine.Add):
// it generates them as Generate does, and runs the engine's full gate on
// the result. The package incremental has the rest of incremental
// merging (Existing, HasConfiguration and AddedFileName among it), as it
// can't import the engine.
func Add(ctx context.Context, tf, rootTF tfrun.Terraform, staging, root string, imports []rootfile.Import, opts Options, existing map[External]string) (*Result, error) {
	return incremental.Engine{
		Generate:     generate,
		FullGate:     FullGate,
		AddRootAlias: addRootAlias,
	}.Add(ctx, tf, rootTF, staging, root, imports, opts, existing)
}
