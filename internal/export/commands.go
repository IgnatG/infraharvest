// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/version"
)

// The documents of the runs of discover and verify, which leave no report
// of the import's kind. A discover run is what its saved inventories say,
// and a verify run what verify.json says.

// withTimes sets when the run started and finished, as those that know them
// pass them.
func withTimes(doc *driftRunDoc, started, finished time.Time) *driftRunDoc {
	if !started.IsZero() {
		doc.StartedAt = formatTime(started)
	}
	if !finished.IsZero() {
		doc.FinishedAt = formatTime(finished)
	}
	return doc
}

// buildFromDiscover builds the document of opts from the inventories discover
// saved in the output directory: the run document, or the OCSF inventory of
// the resources it listed. The inventories hold no owner, so the OCSF
// inventory says nothing of who manages a resource: each is unmanaged, with
// no basis, and the selection file is where discover says which the default
// rules leave out. Its run_id is made from the inventories' bytes, as an
// import's is from report.json.
func buildFromDiscover(opts Options) (any, error) {
	d, err := readDiscovery(opts.Out)
	if err != nil {
		return nil, err
	}
	if d.files == 0 {
		return nil, fmt.Errorf("no inventory in %s: run discover first", filepath.Join(opts.Out, inventoryDir))
	}
	runID := runID(d.content)
	if opts.Document == DocRun {
		return buildDiscoverRun(d, opts, runID), nil
	}
	// What the OCSF inventory takes of a report: the tool's version, and the
	// cloud, which the scopes of the resources say, or else the provider.
	r := &report.Report{Tool: report.Component{Name: "infraharvest", Version: version.Version}}
	switch d.provider {
	case "aws":
		r.Provider.Source = "hashicorp/aws"
	case "azure", "azurerm":
		r.Provider.Source = "hashicorp/azurerm"
	case "google":
		r.Provider.Source = "hashicorp/google"
	}
	return buildOCSFInventory(r, opts, runID, generatedAt(opts))
}

// buildDiscoverRun is the run document of a discover run: ok if every
// listing worked, partial if some failed in part, failed if nothing was
// listed. The failures themselves are not in it.
func buildDiscoverRun(d *discovery, opts Options, runID string) *driftRunDoc {
	doc := &driftRunDoc{Schema: schemaRun, RunID: runID, RequestID: opts.RequestID, Command: CommandDiscover, Outcome: outcomeOK}
	if d.failed {
		doc.Outcome = outcomePartial
		doc.FailureCodes = []string{failRun}
		if len(d.resources) == 0 {
			doc.Outcome = outcomeFailed
		}
	}
	return withTimes(doc, opts.StartedAt, opts.FinishedAt)
}

// verifyRead is what export reads of verify.json: a projection, not the
// file. Each root's findings and its checks' details, which name addresses
// and say why, are not among the fields, so the decoder skips them and no
// code here can reach them.
type verifyRead []struct {
	Path        string         `json:"path"`
	Environment string         `json:"environment"`
	Error       string         `json:"error"`
	Checks      []report.Check `json:"checks"`
}

// readVerify reads the verify.json that infraharvest verify wrote into the
// output directory out, and returns it with its bytes, which name the run.
func readVerify(out string) (verifyRead, []byte, error) {
	content, err := os.ReadFile(filepath.Join(out, report.Dir, report.VerifyJSONFile))
	if err != nil {
		return nil, nil, err
	}
	var v verifyRead
	if err := json.Unmarshal(content, &v); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", report.VerifyJSONFile, err)
	}
	if len(v) == 0 {
		return nil, nil, errors.New(report.VerifyJSONFile + " lists no roots")
	}
	return v, content, nil
}

// buildFromVerify builds the document of opts from verify.json: the run
// document, or the gate. Its run_id is made from verify.json.
func buildFromVerify(opts Options) (any, error) {
	v, content, err := readVerify(opts.Out)
	if err != nil {
		return nil, err
	}
	runID := runID(content)
	switch opts.Document {
	case DocRun:
		return buildVerifyRun(v, opts, runID), nil
	case DocOCSFFindings:
		return buildFindingsOfVerify(v, opts, runID, generatedAt(opts))
	}
	return buildVerifyGate(v, runID), nil
}

// notChecked counts the roots verify couldn't check.
func (v verifyRead) notChecked() int {
	n := 0
	for _, root := range v {
		if root.Error != "" {
			n++
		}
	}
	return n
}

// buildVerifyGate is the gate of the checks of every root verify ran. It
// didn't pass if a root couldn't be checked, whatever the others' checks say.
func buildVerifyGate(v verifyRead, runID string) *gateDoc {
	groups := make([][]report.Check, 0, len(v))
	for _, root := range v {
		groups = append(groups, root.Checks)
	}
	doc := gateOf(groups, runID)
	if v.notChecked() > 0 {
		doc.Passed = false
	}
	return doc
}

// buildVerifyRun is the run document of a verify run: ok if every root was
// checked and every check passed, failed if a check failed or no root could
// be checked, partial if some roots couldn't be checked and no check
// failed. It has no exit code, which verify gives the step and not the
// file.
func buildVerifyRun(v verifyRead, opts Options, runID string) *driftRunDoc {
	doc := &driftRunDoc{Schema: schemaRun, RunID: runID, RequestID: opts.RequestID, Command: CommandVerify, Outcome: outcomeOK}
	notChecked := v.notChecked()
	failed := false
	for _, root := range v {
		for _, c := range root.Checks {
			failed = failed || !c.Passed
		}
	}
	if failed {
		doc.Outcome = outcomeFailed
		doc.FailureCodes = append(doc.FailureCodes, failCheck)
	}
	if notChecked > 0 {
		if !failed {
			doc.Outcome = outcomePartial
			if notChecked == len(v) {
				doc.Outcome = outcomeFailed
			}
		}
		doc.FailureCodes = append(doc.FailureCodes, failRootsNotChecked)
	}
	return withTimes(doc, opts.StartedAt, opts.FinishedAt)
}
