// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"errors"

	"github.com/IgnatG/infraharvest/internal/selection"
)

// The modes of a run are values of Options, so that what can't go
// together can't be set: Mode says what the run lists and imports, Roots
// what it does with the roots earlier runs generated. --resume goes with
// any Roots, and --reuse-inventory, which only import reads, with any
// Mode of an import, so they stay flags of Options. What the types
// can't say, an import that names a selection file and --all, or none
// of them, checkModes refuses.

// Mode is what a run lists, and whether it imports.
type Mode int

const (
	// ModeSelection imports what a selection file includes, or the
	// CloudFormation stacks the command names (--selection).
	ModeSelection Mode = iota
	// ModeAll imports what the default selection includes, without a
	// selection file (--all).
	ModeAll
	// ModeDiscover lists resources into the selection file instead of
	// importing them (infraharvest discover).
	ModeDiscover
)

// String names m as the flag or command that sets it.
func (m Mode) String() string {
	switch m {
	case ModeSelection:
		return "selection"
	case ModeAll:
		return "all"
	case ModeDiscover:
		return "discover"
	}
	return "unknown"
}

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

// String names r as the flag that sets it, "" for RootsFresh.
func (r RootsMode) String() string {
	switch r {
	case RootsRegenerate:
		return "regenerate"
	case RootsIncremental:
		return "incremental"
	}
	return ""
}

// discovers reports whether the run lists resources into the selection
// file instead of importing them.
func (o Options) discovers() bool { return o.Mode == ModeDiscover }

// checkModes refuses an import that doesn't say what to import: a
// selection file, --all, or the CloudFormation stacks to take over. An
// import without any could bring a whole account under Terraform by
// accident. Discover takes none of them: --selection is the file it writes.
func checkModes(options Options) error {
	switch {
	case options.discovers():
		return nil
	case options.Selection != "" && options.Mode == ModeAll:
		return errors.New("use either --selection or --all")
	case options.Selection == "" && options.Mode != ModeAll && !options.stackScoped():
		return selection.ErrNoSelection
	}
	return nil
}

// readsEarlierRoots reports whether the run reads the roots earlier runs
// generated, to keep, extend or generate them again: --incremental,
// --resume or --regenerate.
func (o Options) readsEarlierRoots() bool {
	return o.Roots != RootsFresh || o.Resume
}
