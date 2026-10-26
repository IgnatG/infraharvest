// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"net/http"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// callTools are what an Import call reaches outside the process through,
// which tests replace (see Options.tools): the engine binary, the
// registry the root files pin the provider from, and the engine that
// writes each root, whether it generates, adds to or vacates it, or
// merges the roots of a stack's environments, or vacates their merged
// root. A nil *callTools, or a nil field, uses the real one.
type callTools struct {
	// findBinary returns the path of the engine binary, as
	// engine.Binary.Find does.
	findBinary func(ctx context.Context, binary engine.Binary, explicitPath, cacheDir string) (string, error)
	// rootConfig renders the root files, as rootConfig does with the
	// default registry.
	rootConfig func(ctx context.Context, registry, execPath string, p engine.Provider, constraints []moduleConstraint) (*rootFiles, error)
	// generate generates a root, as generateDir does.
	generate func(ctx context.Context, dir string, tr terraformRun, imports []engine.Import, opts engine.Options, lock *[]byte) (*engine.Result, error)
	// addTo adds to a root, as engine.Add does.
	addTo func(ctx context.Context, tf, rootTF engine.Terraform, staging, root string, imports []engine.Import, opts engine.Options, existing map[engine.External]string) (*engine.Result, error)
	// vacateRoot writes a root with removed blocks alone, as engine.Vacate
	// does.
	vacateRoot func(dir string, opts engine.Options) (*engine.Result, error)
	// merge merges the roots of a stack's environments, as
	// engine.MergeRoots does.
	merge func(ctx context.Context, dir string, roots []engine.MergeRoot, opts engine.MergeOptions) (*engine.MergeResult, error)
	// vacateMergedRoot writes a merged root with removed blocks alone, as
	// engine.VacateMerged does.
	vacateMergedRoot func(dir string, files []string, removed []engine.Removal) ([]string, engine.Check, error)
}

// find returns the path of binary (see engine.Binary.Find).
func (t *callTools) find(ctx context.Context, binary engine.Binary, explicitPath, cacheDir string) (string, error) {
	if t != nil && t.findBinary != nil {
		return t.findBinary(ctx, binary, explicitPath, cacheDir)
	}
	return binary.Find(ctx, explicitPath, cacheDir)
}

// root renders the root files of every output directory (see rootConfig).
func (t *callTools) root(ctx context.Context, registry, execPath string, p engine.Provider, constraints []moduleConstraint) (*rootFiles, error) {
	if t != nil && t.rootConfig != nil {
		return t.rootConfig(ctx, registry, execPath, p, constraints)
	}
	return rootConfig(ctx, http.DefaultClient, registry, "", execPath, p, constraints)
}

// generateDir generates the root in dir (see generateDir).
func (t *callTools) generateDir(ctx context.Context, dir string, tr terraformRun, imports []engine.Import, opts engine.Options, lock *[]byte) (*engine.Result, error) {
	if t != nil && t.generate != nil {
		return t.generate(ctx, dir, tr, imports, opts, lock)
	}
	return generateDir(ctx, dir, tr, imports, opts, lock)
}

// add adds imports to the root in root, generating them in staging (see
// engine.Add).
func (t *callTools) add(ctx context.Context, tf, rootTF engine.Terraform, staging, root string, imports []engine.Import, opts engine.Options, existing map[engine.External]string) (*engine.Result, error) {
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

// mergeRoots merges roots into the root in dir (see engine.MergeRoots).
func (t *callTools) mergeRoots(ctx context.Context, dir string, roots []engine.MergeRoot, opts engine.MergeOptions) (*engine.MergeResult, error) {
	if t != nil && t.merge != nil {
		return t.merge(ctx, dir, roots, opts)
	}
	return engine.MergeRoots(ctx, dir, roots, opts)
}

// vacateMerged writes the merged root in dir with removed alone (see
// engine.VacateMerged).
func (t *callTools) vacateMerged(dir string, files []string, removed []engine.Removal) ([]string, engine.Check, error) {
	if t != nil && t.vacateMergedRoot != nil {
		return t.vacateMergedRoot(dir, files, removed)
	}
	return engine.VacateMerged(dir, files, removed)
}
