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
// the report by their IDs (see gateOf): a check passed if none of its runs failed, and
// was skipped if all of them were; its reason codes are those of the runs
// that failed or were skipped.
func buildGate(r *report.Report, runID string) *gateDoc {
	var groups [][]report.Check
	for _, d := range r.Directories {
		groups = append(groups, d.Checks)
	}
	for _, m := range r.Merges {
		groups = append(groups, m.Checks)
	}
	return gateOf(groups, runID)
}

// checkSummary is what the runs of one check say: how many ran, how many
// failed and how many were skipped, and the reason codes of those that did.
type checkSummary struct {
	failed, run, skipped int
	codes                map[string]bool
}

// passed says no run of the check failed.
func (s *checkSummary) passed() bool { return s.failed == 0 }

// wasSkipped says every run of the check was skipped.
func (s *checkSummary) wasSkipped() bool { return s.skipped == s.run }

// reasonCodes returns the codes, sorted; non-nil, as the schemas want a list.
func (s *checkSummary) reasonCodes() []string {
	codes := make([]string, 0, len(s.codes))
	for code := range s.codes {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// checkSummaries summarise checks by their IDs, G1 to G8.
type checkSummaries map[string]*checkSummary

// add summarises the runs of checks, which are those of one root or more.
// Checks that are not of the gate are not counted.
func (sums checkSummaries) add(checks []report.Check) {
	for _, c := range checks {
		id := checkID(c.Name)
		if id == "" {
			continue
		}
		s := sums[id]
		if s == nil {
			s = &checkSummary{codes: map[string]bool{}}
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

// ids returns the IDs of the checks summarised, sorted.
func (sums checkSummaries) ids() []string {
	ids := make([]string, 0, len(sums))
	for id := range sums {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// gateOf summarises the checks of each of groups, the checks of one root,
// as buildGate does.
func gateOf(groups [][]report.Check, runID string) *gateDoc {
	sums := checkSummaries{}
	for _, checks := range groups {
		sums.add(checks)
	}
	doc := &gateDoc{Schema: schemaGate, RunID: runID, Passed: len(sums) > 0, Checks: []gateCheck{}}
	for _, id := range sums.ids() {
		s := sums[id]
		check := gateCheck{ID: id, Passed: s.passed(), Skipped: s.wasSkipped(), ReasonCodes: s.reasonCodes()}
		if !check.Passed {
			doc.Passed = false
		}
		doc.Checks = append(doc.Checks, check)
	}
	return doc
}
