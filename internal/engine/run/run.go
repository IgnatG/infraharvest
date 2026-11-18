// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package run holds what the engine's passes share: the options and result
// of one run of the pipeline, the workspace of configuration files it edits,
// and the types the options and result are made of.
package run

import "github.com/IgnatG/infraharvest/internal/engine/secret"

// Run is one run of the pipeline, Generate's, of a root of its own (see
// Generating), or Add's, of resources to add to a root an earlier import
// generated: the Options it runs with, which for Add's differ from the
// caller's, and what it finds on the way, which the passes receive with it.
// The passes and the gate of the directory the run generates in read its
// Options; Add checks the root it adds to with the caller's own (see Add).
type Run struct {
	Opts Options
	// Taken are names the run leaves alone, such as those of the root an
	// add adds to.
	Taken Names
	// Secrets gives the run's own plans the values of the secrets it found
	// in the configuration (see secret.LiteralsToVariables), once it found
	// any.
	Secrets *secret.Values
	// Modules counts the users of the generated modules the run takes up
	// (see Options.Modules).
	Modules *ModuleRefs
}

// Generating is Generate's run with opts: a root of its own, with the
// settings opts gives.
func Generating(opts Options) *Run {
	modules := opts.Modules
	if modules == nil {
		modules = &ModuleRefs{}
	}
	return &Run{Opts: opts, Modules: modules}
}
