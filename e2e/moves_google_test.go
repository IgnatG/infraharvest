// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || google)

package e2e

import (
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// TestGoogleRegenerateMoves imports testdata/google-moves, three buckets,
// one root per environment (--env-tags=env), and applies the root, keeping
// its state, as a team would. It then gives one bucket another env label,
// so another root, and generates the roots again (--regenerate): the root
// it applied hands the bucket over with a removed block, and the other
// environment's root imports it (see handover.check). A bucket's name is
// its ID, so no bucket gets another address: moved blocks are the AWS
// test's.
func TestGoogleRegenerateMoves(t *testing.T) {
	e := seedGoogle(t)
	fixture := seedApplied(e.ctx, t, "google-moves", e.execPath, e.plugins)
	useAssetSearch(t, e, fixture.resources(e.ctx, t))
	out := t.TempDir()
	handover{
		ctx: e.ctx, execPath: e.execPath, plugins: e.plugins,
		typ:    "google_storage_bucket",
		handed: stateValue(t, fixture.resources(e.ctx, t), "google_storage_bucket", "handed", "name"),
		kept:   stateValue(t, fixture.resources(e.ctx, t), "google_storage_bucket", "kept", "name"),
		args: []string{
			"import", "google",
			"--engine=" + e.engineName,
			"--terraform-path=" + e.execPath,
			"--projects=" + googleProject,
			"--services=gcs",
			"--regions=global,us-central1",
			"--env-tags=env",
			// No module calls: each bucket is a resource of the root, at
			// the address the report gives it.
			"--modules=none",
			"--out=" + out,
			"--all",
		},
		out: out,
		change: func(t *testing.T) {
			fixture.apply(e.ctx, t, tfexec.Var("handed_env=prod"))
			// A stand-in of its own, whose search the import doesn't
			// have from before, with the bucket's new label.
			useAssetSearch(t, e, fixture.resources(e.ctx, t))
		},
	}.check(t)
}
