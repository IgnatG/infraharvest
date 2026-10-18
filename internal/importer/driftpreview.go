// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"github.com/infraharvest/infraharvest-cli/internal/engine"
	"github.com/infraharvest/infraharvest-cli/internal/report"
)

// DriftPreview counts what a decisions file and rules would decide of the
// changes a run without them found, before a run with them writes
// anything: for a person to confirm.
type DriftPreview struct {
	// Accept counts the changes the next apply would revert that would be
	// accepted: written into the code, unless they are secrets, take a
	// hand edit, or the plan of the edited code rejects them.
	Accept int
	// Keep counts the changes the next apply would revert that would stay
	// as they are.
	Keep int
	// Stale counts the changes whose entry in the decisions file was
	// decided for other values, so that it isn't applied.
	Stale int
}

// PreviewDrift decides changes, those of a run with neither a decisions
// file nor rules, as a run with decisions (nil for none) and rules would,
// and counts them. A change of the run with them may still differ, if
// the cloud changes in between.
func PreviewDrift(decisions *DriftDecisions, rules []DriftRule, changes []report.DriftChange) DriftPreview {
	decider := driftDecider{file: decisions, rules: rules}
	var p DriftPreview
	for _, c := range changes {
		// Without decisions, every change the next apply would revert is
		// kept: the others have nothing to decide.
		if c.Outcome != report.DriftKept {
			continue
		}
		d := decider.decide(c.Root, engine.DriftChange{
			Address: c.Address, Type: c.Type, ModuleAddress: c.ModuleCall, Attribute: c.Attribute,
			Before: c.Before, After: c.After, Kind: c.Kind, Covers: c.Covers,
		})
		if d.accept {
			p.Accept++
		} else {
			p.Keep++
		}
		if d.stale {
			p.Stale++
		}
	}
	return p
}
