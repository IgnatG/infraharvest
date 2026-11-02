// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"sort"
	"strings"

	"github.com/IgnatG/infraharvest/internal/report"
)

// The reason codes of a check (see schemas/gate-v1.schema.json). They are
// a closed list: a check's own details are free text, with addresses and
// values in them, and never go into the export.
const (
	reasonNotFormatted  = "not_formatted"
	reasonInvalid       = "invalid_configuration"
	reasonPlanFailed    = "plan_failed"
	reasonStandards     = "standards_violation"
	reasonScanners      = "scanner_findings"
	reasonSecrets       = "secrets_found"
	reasonNotDeterm     = "not_deterministic"
	reasonMovesInvalid  = "moves_invalid"
	reasonToolMissing   = "tool_missing"
	reasonNotRun        = "not_run"
	reasonOther         = "other"
	scannersMissingNote = "no scanner installed"
)

// failedReason is the reason code of a check that failed, by its ID.
var failedReason = map[string]string{
	"G1": reasonNotFormatted,
	"G2": reasonInvalid,
	"G3": reasonPlanFailed,
	"G4": reasonStandards,
	"G5": reasonScanners,
	"G6": reasonSecrets,
	"G7": reasonNotDeterm,
	"G8": reasonMovesInvalid,
}

// checkID returns G1 to G8 of a check named like "G6 secrets", or "".
func checkID(name string) string {
	id, _, _ := strings.Cut(name, " ")
	if _, ok := failedReason[id]; ok {
		return id
	}
	return ""
}

// reasonCodes returns the reason codes of one run of a check that failed
// or was skipped. Only the shape of its details is read, never kept; what
// is not known becomes other.
func reasonCodes(id string, c report.Check) []string {
	details := strings.Join(c.Details, "\n")
	switch {
	case !c.Passed && strings.Contains(details, "didn't run"):
		return []string{reasonNotRun}
	case !c.Passed:
		return []string{failedReason[id]}
	case c.Skipped && strings.Contains(details, scannersMissingNote):
		return []string{reasonToolMissing}
	case c.Skipped && (strings.Contains(details, "not run") || strings.Contains(details, "not planned")):
		return []string{reasonNotRun}
	case c.Skipped:
		return []string{reasonOther}
	}
	return nil
}

// buildGate summarises the checks of every directory and merged root of
// the report by their IDs: a check passed if none of its runs failed, and
// was skipped if all of them were; its reason codes are those of the runs
// that failed or were skipped.
func buildGate(r *report.Report, runID string) *gateDoc {
	type summary struct {
		failed, run, skipped int
		codes                map[string]bool
	}
	sums := map[string]*summary{}
	add := func(checks []report.Check) {
		for _, c := range checks {
			id := checkID(c.Name)
			if id == "" {
				continue
			}
			s := sums[id]
			if s == nil {
				s = &summary{codes: map[string]bool{}}
				sums[id] = s
			}
			s.run++
			if !c.Passed {
				s.failed++
			}
			if c.Skipped {
				s.skipped++
			}
			for _, code := range reasonCodes(id, c) {
				s.codes[code] = true
			}
		}
	}
	for _, d := range r.Directories {
		add(d.Checks)
	}
	for _, m := range r.Merges {
		add(m.Checks)
	}
	doc := &gateDoc{Schema: schemaGate, RunID: runID, Passed: len(sums) > 0, Checks: []gateCheck{}}
	ids := make([]string, 0, len(sums))
	for id := range sums {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := sums[id]
		check := gateCheck{ID: id, Passed: s.failed == 0, Skipped: s.skipped == s.run, ReasonCodes: []string{}}
		for code := range s.codes {
			check.ReasonCodes = append(check.ReasonCodes, code)
		}
		sort.Strings(check.ReasonCodes)
		if !check.Passed {
			doc.Passed = false
		}
		doc.Checks = append(doc.Checks, check)
	}
	return doc
}
