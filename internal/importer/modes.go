// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"errors"

	"github.com/IgnatG/infraharvest/internal/selection"
)

// The modes of a run are set by flags of Options that exclude one another
// in part. checkModes refuses what doesn't go together, in one place, and
// the methods below read the modes the code asks about, rather than each
// place combining the flags again.

// RootsMode is what an import does with the roots earlier runs generated
// in --out.
type RootsMode int

const (
	// RootsFresh generates the roots as a first import would.
	RootsFresh RootsMode = iota
	// RootsRegenerate generates the roots earlier runs generated again,
	// from scratch (--regenerate).
	RootsRegenerate
	// RootsIncremental adds what is new to the roots earlier runs
	// generated (--incremental).
	RootsIncremental
)

// checkModes refuses modes that don't go together, and an import that
// doesn't say what to import: a selection file, --all, or the
// CloudFormation stacks to take over. An import without any could bring
// a whole account under Terraform by accident. --resume goes with either
// roots mode, and --reuse-inventory, which only import reads, with all.
func checkModes(options Options) error {
	switch {
	case options.Regenerate && options.Incremental:
		return errors.New("use either --regenerate or --incremental")
	case options.Discover:
		if options.All {
			return errors.New("discover lists everything; --all is for import")
		}
		return nil
	case options.Selection != "" && options.All:
		return errors.New("use either --selection or --all")
	case options.Selection == "" && !options.All && !options.stackScoped():
		return selection.ErrNoSelection
	}
	return nil
}

// Roots returns what the run does with the roots earlier runs generated,
// of options that checkModes passed.
func (o Options) Roots() RootsMode {
	switch {
	case o.Regenerate:
		return RootsRegenerate
	case o.Incremental:
		return RootsIncremental
	}
	return RootsFresh
}

// readsEarlierRoots reports whether the run reads the roots earlier runs
// generated, to keep, extend or generate them again: --incremental,
// --resume or --regenerate.
func (o Options) readsEarlierRoots() bool {
	return o.Roots() != RootsFresh || o.Resume
}
