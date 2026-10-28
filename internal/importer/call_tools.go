// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"net/http"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/tfquery"
)

// callTools are what an Import call reaches outside the process through,
// which tests replace (see Run.tools): the engine binary, the
// registry the root files pin the provider from and the modules' releases
// are looked up in, and the engine that
// writes each root, whether it generates, adds to or vacates it, or
// writes the README of a root added to, or merges the roots of a stack's
// environments, or writes what their merged root no longer finds in the
// cloud, or its moved blocks, or vacates it; and Terraform's list
// resources, which the query service lists through. A nil *callTools, or
// a nil field, uses the real one.
type callTools struct {
	// findBinary returns the path of the engine binary, as
	// tfrun.Binary.Find does.
	findBinary func(ctx context.Context, binary tfrun.Binary, explicitPath, cacheDir string) (string, error)
	// rootConfig renders the root files, as rootConfig does with the
	// default registry.
	rootConfig func(ctx context.Context, registry, execPath string, p rootfile.Provider, constraints []moduleConstraint) (*rootFiles, error)
	// moduleReleases lists the releases of a registry module, as
	// engine.ModuleReleases does with the default registry.
	moduleReleases func(ctx context.Context, source string) ([]engine.ModuleRelease, error)
	// generate generates a root, as generateDir does.
	generate func(ctx context.Context, dir string, tr terraformRun, imports []rootfile.Import, opts engine.Options, lock *[]byte) (*engine.Result, error)
	// addTo adds to a root, as engine.Add does.
	addTo func(ctx context.Context, tf, rootTF tfrun.Terraform, staging, root string, imports []rootfile.Import, opts engine.Options, existing map[engine.External]string) (*engine.Result, error)
	// vacateRoot writes a root with removed blocks alone, as engine.Vacate
	// does.
	vacateRoot func(dir string, opts engine.Options) (*engine.Result, error)
	// readme writes the README of a root, as engine.WriteReadme does.
	readme func(dir string, result *engine.Result) error
	// merge merges the roots of a stack's environments, as
	// engine.MergeRoots does.
	merge func(ctx context.Context, dir string, roots []engine.MergeRoot, opts engine.MergeOptions) (*engine.MergeResult, error)
	// mergedNotInCloud lists in a merged root's README what is no longer
	// in the cloud, as engine.WriteMergedNotInCloud does.
	mergedNotInCloud func(dir string, gone map[string][]rootfile.Import) error
	// mergedMoves writes the moved and removed blocks of a merged root,
	// as engine.WriteMergedMoves does.
	mergedMoves func(dir string, moved []rootfile.Move, removed []rootfile.Removal) (gate.Check, error)
	// vacateMergedRoot writes a merged root with removed blocks alone, as
	// engine.VacateMerged does.
	vacateMergedRoot func(dir string, files []string, removed []rootfile.Removal) ([]string, gate.Check, error)
	// query lists through Terraform's list resources, as tfquery.List
	// does.
	query func(ctx context.Context, req tfquery.Request) ([]harvest.Resource, error)
}

// listQuery lists through Terraform's list resources (see tfquery.List).
func (t *callTools) listQuery(ctx context.Context, req tfquery.Request) ([]harvest.Resource, error) {
	if t != nil && t.query != nil {
		return t.query(ctx, req)
	}
	return tfquery.List(ctx, req)
}

// find returns the path of binary (see tfrun.Binary.Find).
func (t *callTools) find(ctx context.Context, binary tfrun.Binary, explicitPath, cacheDir string) (string, error) {
	if t != nil && t.findBinary != nil {
		return t.findBinary(ctx, binary, explicitPath, cacheDir)
	}
	return binary.Find(ctx, explicitPath, cacheDir)
}

// root renders the root files of every output directory (see rootConfig).
func (t *callTools) root(ctx context.Context, registry, execPath string, p rootfile.Provider, constraints []moduleConstraint) (*rootFiles, error) {
	if t != nil && t.rootConfig != nil {
		return t.rootConfig(ctx, registry, execPath, p, constraints)
	}
	return rootConfig(ctx, http.DefaultClient, registry, "", execPath, p, constraints)
}

// listModuleReleases lists the releases of the registry module at source
// (see engine.ModuleReleases).
func (t *callTools) listModuleReleases(ctx context.Context, source string) ([]engine.ModuleRelease, error) {
	if t != nil && t.moduleReleases != nil {
		return t.moduleReleases(ctx, source)
	}
	return engine.ModuleReleases(ctx, http.DefaultClient, "", source)
}

// generateDir generates the root in dir (see generateDir).
func (t *callTools) generateDir(ctx context.Context, dir string, tr terraformRun, imports []rootfile.Import, opts engine.Options, lock *[]byte) (*engine.Result, error) {
	if t != nil && t.generate != nil {
		return t.generate(ctx, dir, tr, imports, opts, lock)
	}
	return generateDir(ctx, dir, tr, imports, opts, lock)
}

// add adds imports to the root in root, generating them in staging (see
// engine.Add).
func (t *callTools) add(ctx context.Context, tf, rootTF tfrun.Terraform, staging, root string, imports []rootfile.Import, opts engine.Options, existing map[engine.External]string) (*engine.Result, error) {
	if t != nil && t.addTo != nil {
		return t.addTo(ctx, tf, rootTF, staging, root, imports, opts, existing)
	}
	return engine.Add(ctx, tf, rootTF, staging, root, imports, opts, existing)
}

// vacate writes the root in dir with the removed blocks of opts alone
// (see engine.Vacate).
func (t *callTools) vacate(dir string, opts engine.Options) (*engine.Result, error) {
	if t != nil && t.vacateRoot != nil {
		return t.vacateRoot(dir, opts)
	}
	return engine.Vacate(dir, opts)
}

// writeReadme writes the README of the root in dir, for result (see
// engine.WriteReadme).
func (t *callTools) writeReadme(dir string, result *engine.Result) error {
	if t != nil && t.readme != nil {
		return t.readme(dir, result)
	}
	return engine.WriteReadme(dir, result)
}

// mergeRoots merges roots into the root in dir (see engine.MergeRoots).
func (t *callTools) mergeRoots(ctx context.Context, dir string, roots []engine.MergeRoot, opts engine.MergeOptions) (*engine.MergeResult, error) {
	if t != nil && t.merge != nil {
		return t.merge(ctx, dir, roots, opts)
	}
	return engine.MergeRoots(ctx, dir, roots, opts)
}

// vacateMerged writes the merged root in dir with removed alone (see
// engine.VacateMerged).
func (t *callTools) vacateMerged(dir string, files []string, removed []rootfile.Removal) ([]string, gate.Check, error) {
	if t != nil && t.vacateMergedRoot != nil {
		return t.vacateMergedRoot(dir, files, removed)
	}
	return engine.VacateMerged(dir, files, removed)
}

// writeMergedNotInCloud lists gone in the README of the merged root in
// dir (see engine.WriteMergedNotInCloud).
func (t *callTools) writeMergedNotInCloud(dir string, gone map[string][]rootfile.Import) error {
	if t != nil && t.mergedNotInCloud != nil {
		return t.mergedNotInCloud(dir, gone)
	}
	return engine.WriteMergedNotInCloud(dir, gone)
}

// writeMergedMoves writes moved and removed into the merged root in dir,
// and returns their moves check (see engine.WriteMergedMoves).
func (t *callTools) writeMergedMoves(dir string, moved []rootfile.Move, removed []rootfile.Removal) (gate.Check, error) {
	if t != nil && t.mergedMoves != nil {
		return t.mergedMoves(dir, moved, removed)
	}
	return engine.WriteMergedMoves(dir, moved, removed)
}
