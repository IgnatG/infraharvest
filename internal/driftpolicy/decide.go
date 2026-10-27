// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package driftpolicy

import "github.com/IgnatG/infraharvest/internal/report"

// Decider decides the changes of a run: by the decisions file, if one is
// applied, then by the rules of --accept and --keep.
type Decider struct {
	// Decisions is the decisions file --decisions applies, or nil.
	Decisions *Decisions
	// Rules are those of --accept and --keep, in order.
	Rules []Rule
}

// Decision is what a Decider decided for a change.
type Decision struct {
	// Accept says to write the cloud's value into the code; otherwise the
	// code keeps its own.
	Accept bool
	// Source says what decided it, as report.DriftChange.DecisionSource
	// has it.
	Source string
	// Stale says the change's entry in the decisions file was decided
	// for other values.
	Stale bool
}

// Active reports whether d decides anything: without a decisions file or
// rules, every change is kept.
func (d Decider) Active() bool {
	return d.Decisions != nil || len(d.Rules) > 0
}

// Decide decides c, a change of the root c.Root, as environment
// c.Environment for a root environments share, whose entry in the
// decisions file is that environment's. Without a decisions file it is
// kept. With one, its defaults decide it, then its rules (the last that
// matches), then its entry, unless the entry is undecided, which leaves
// it to the rules and defaults. An entry whose fingerprint no longer
// matches the change keeps it, whatever the rules and defaults say. The
// rules of --accept and --keep come last, the last that matches
// deciding: on the command line, they decide over the file, a stale
// entry's too.
func (d Decider) Decide(c report.DriftChange) Decision {
	out := Decision{Source: report.DriftDecidedByDefault}
	if f := d.Decisions; f != nil {
		out.Accept, out.Source = f.Defaults.Decision == report.DriftAccept, report.DriftDecidedByDefaults
		for _, r := range f.Rules {
			switch {
			case r.Accept != nil && r.Accept.matches(c):
				out.Accept, out.Source = true, report.DriftDecidedByRule
			case r.Keep != nil && r.Keep.matches(c):
				out.Accept, out.Source = false, report.DriftDecidedByRule
			}
		}
		if e := f.entry(c); e != nil {
			switch {
			case e.Fingerprint != fingerprint(c):
				// The entry was decided for other values: neither it nor
				// the file's rules and defaults decide what the change is
				// now, so it is kept until someone decides again.
				out.Accept, out.Source, out.Stale = false, report.DriftDecidedByEntry, true
			case e.Decision != Undecided:
				out.Accept, out.Source = e.Decision == report.DriftAccept, report.DriftDecidedByEntry
			}
		}
	}
	for _, r := range d.Rules {
		if r.matches(c) {
			out.Accept, out.Source = r.Accept, report.DriftDecidedByFlag
		}
	}
	return out
}

// Preview counts what d would decide of the changes a run without a
// decisions file or rules found, before a run with them writes anything:
// for a person to confirm.
type Preview struct {
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

// Preview decides changes, those of a run with neither a decisions file
// nor rules, as a run with d's would, and counts them. A change of the
// run with them may still differ, if the cloud changes in between.
func (d Decider) Preview(changes []report.DriftChange) Preview {
	var p Preview
	for _, c := range changes {
		// Without decisions, every change the next apply would revert is
		// kept: the others have nothing to decide.
		if c.Outcome != report.DriftKept {
			continue
		}
		decision := d.Decide(c)
		if decision.Accept {
			p.Accept++
		} else {
			p.Keep++
		}
		if decision.Stale {
			p.Stale++
		}
	}
	return p
}
