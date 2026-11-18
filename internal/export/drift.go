// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/IgnatG/infraharvest/internal/report"
)

// failRootsNotChecked is the failure code of a drift run that couldn't
// check a root (see schemas/run-v1.schema.json).
const failRootsNotChecked = "roots_not_checked"

// driftRead is what export reads of drift.json: a projection, not the
// report. It names the fields export may use. The values of each change,
// what the code sets, the reasons and who changed it are not among them, so
// the decoder skips them and no code here can reach them.
type driftRead struct {
	Schema        string `json:"schema"`
	SchemaVersion int    `json:"schema_version"`
	DriftFound    bool   `json:"drift_found"`
	Roots         []struct {
		Path        string `json:"path"`
		Environment string `json:"environment"`
		Error       string `json:"error"`
	} `json:"roots"`
}

// readDrift reads the drift.json that infraharvest drift wrote into the
// output directory out, and returns it with its bytes, which name the run.
func readDrift(out string) (*driftRead, []byte, error) {
	content, err := os.ReadFile(filepath.Join(out, report.Dir, report.DriftJSONFile))
	if err != nil {
		return nil, nil, err
	}
	var d driftRead
	if err := json.Unmarshal(content, &d); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", report.DriftJSONFile, err)
	}
	if d.SchemaVersion != report.SchemaVersion {
		return nil, nil, fmt.Errorf("%s: schema version %d, want %d", report.DriftJSONFile, d.SchemaVersion, report.SchemaVersion)
	}
	// Files written before the schema field have none.
	if d.Schema != "" && d.Schema != report.DriftSchema {
		return nil, nil, fmt.Errorf("%s: schema %q, want %q", report.DriftJSONFile, d.Schema, report.DriftSchema)
	}
	return &d, content, nil
}

// buildDriftRun is the run document of the drift run d reports: ok if it
// checked every root, partial if it couldn't check some, failed if it
// couldn't check any. Drift found in the cloud isn't a failure of the run.
func buildDriftRun(d *driftRead, runID string, started, finished time.Time) *driftRunDoc {
	doc := &driftRunDoc{Schema: schemaRun, RunID: runID, Command: CommandDrift, Outcome: outcomeOK}
	var failed int
	for _, r := range d.Roots {
		if r.Error != "" {
			failed++
		}
	}
	if failed > 0 {
		doc.Outcome = outcomePartial
		doc.FailureCodes = []string{failRootsNotChecked}
		if failed == len(d.Roots) {
			doc.Outcome = outcomeFailed
		}
	}
	if !started.IsZero() {
		doc.StartedAt = formatTime(started)
	}
	if !finished.IsZero() {
		doc.FinishedAt = formatTime(finished)
	}
	return doc
}
