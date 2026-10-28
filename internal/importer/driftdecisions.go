// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/IgnatG/infraharvest/internal/driftpolicy"
	"github.com/IgnatG/infraharvest/internal/engine/drift"
	"github.com/IgnatG/infraharvest/internal/report"
)

// decidedDrift holds the decisions taken for a root's changes, by
// address and attribute, and, for a change of shared tags, by those of
// the resources it covers, whose changes take its decision when its edit
// is left out.
type decidedDrift struct {
	own, covered map[string]driftpolicy.Decision
}

func newDecidedDrift() decidedDrift {
	return decidedDrift{own: map[string]driftpolicy.Decision{}, covered: map[string]driftpolicy.Decision{}}
}

// decidedKey identifies a change of one root by its address and
// attribute.
func decidedKey(address, attribute string) string {
	return address + "\n" + attribute
}

// record records d, decided for c.
func (r decidedDrift) record(c drift.Change, d driftpolicy.Decision) {
	r.own[decidedKey(c.Address, c.Attribute)] = d
	for _, a := range c.Covers {
		r.covered[decidedKey(a, c.Attribute)] = d
	}
}

// of returns the decision taken for c: its own, else that of the change
// of shared tags that covered it, else, for a change nothing decided,
// what decide says.
func (r decidedDrift) of(c drift.Change, decide func() driftpolicy.Decision) driftpolicy.Decision {
	k := decidedKey(c.Address, c.Attribute)
	if d, ok := r.own[k]; ok {
		return d
	}
	if d, ok := r.covered[k]; ok {
		return d
	}
	return decide()
}

// driftDecisionsPath is the decisions file --write-decisions writes: the
// file it names, else --decisions, else driftpolicy.DefaultDecisionsFile
// in the output directory.
func driftDecisionsPath(options DriftOptions) string {
	if options.WriteDecisionsTo != "" {
		return options.WriteDecisionsTo
	}
	if options.Decisions != "" {
		return options.Decisions
	}
	return filepath.Join(options.Out, driftpolicy.DefaultDecisionsFile)
}

// decisionsToMerge returns the decisions file --write-decisions merges
// the run's changes into: applied, the one --decisions read; else the
// one already at its path, or, if there is none, a new one.
func decisionsToMerge(options DriftOptions, applied *driftpolicy.Decisions) (*driftpolicy.Decisions, error) {
	if applied != nil {
		return applied, nil
	}
	path := driftDecisionsPath(options)
	f, err := driftpolicy.LoadDecisions(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return driftpolicy.NewDecisions(), nil
	case err != nil:
		return nil, fmt.Errorf("--write-decisions: %w", err)
	}
	return f, nil
}

// writeDriftDecisions merges the changes of d into f and writes it to
// path, and returns a line for the summary that says so. Entries of the
// roots d couldn't check stay as they are.
func writeDriftDecisions(path string, f *driftpolicy.Decisions, d *report.Drift) (string, error) {
	checked := map[string]bool{}
	for _, r := range d.Roots {
		checked[r.Name()] = r.Error == ""
	}
	added, undecided, dropped := f.Merge(d.Changes, func(root string) bool { return checked[root] })
	if err := f.Save(path); err != nil {
		return "", fmt.Errorf("--write-decisions: %w", err)
	}
	return fmt.Sprintf("decisions: %s (%d added, %d undecided again, %d dropped)", path, added, undecided, dropped), nil
}
