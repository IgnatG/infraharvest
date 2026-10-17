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
// generates each root. A nil *callTools, or a nil field, uses the real
// one.
type callTools struct {
	// findBinary returns the path of the engine binary, as
	// engine.Binary.Find does.
	findBinary func(ctx context.Context, binary engine.Binary, explicitPath, cacheDir string) (string, error)
	// rootConfig renders the root files, as rootConfig does with the
	// default registry.
	rootConfig func(ctx context.Context, registry, execPath string, p engine.Provider, constraints []moduleConstraint) (*rootFiles, error)
	// generate generates a root, as generateDir does.
	generate func(ctx context.Context, dir string, tr terraformRun, imports []engine.Import, opts engine.Options, lock *[]byte) (*engine.Result, error)
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
