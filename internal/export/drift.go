// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/IgnatG/infraharvest/internal/engine/secret"
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
	Changes []struct {
		Root        string   `json:"root"`
		Environment string   `json:"environment"`
		Address     string   `json:"address"`
		Type        string   `json:"type"`
		Covers      []string `json:"covers"`
		Outcome     string   `json:"outcome"`
	} `json:"changes"`
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
// checked every root, partial if it couldn't check some, or if an accepted
// change was rejected, so that nothing was written for it (check_failed, as
// the step exits 1 for it), failed if it couldn't check any. Drift found in
// the cloud isn't a failure of the run.
func buildDriftRun(d *driftRead, runID string, started, finished time.Time) *driftRunDoc {
	doc := &driftRunDoc{Schema: schemaRun, RunID: runID, Command: CommandDrift, Outcome: outcomeOK}
	var failed, rejected int
	for _, r := range d.Roots {
		if r.Error != "" {
			failed++
		}
	}
	for _, c := range d.Changes {
		if c.Outcome == report.DriftRejectedByPlan || c.Outcome == report.DriftRejectedByCheck {
			rejected++
		}
	}
	if failed > 0 {
		doc.Outcome = outcomePartial
		doc.FailureCodes = []string{failRootsNotChecked}
		if failed == len(d.Roots) {
			doc.Outcome = outcomeFailed
		}
	}
	if rejected > 0 {
		if doc.Outcome == outcomeOK {
			doc.Outcome = outcomePartial
		}
		doc.FailureCodes = append(doc.FailureCodes, failCheck)
	}
	if !started.IsZero() {
		doc.StartedAt = formatTime(started)
	}
	if !finished.IsZero() {
		doc.FinishedAt = formatTime(finished)
	}
	return doc
}

// How the addresses of the drift summary are written
// (driftSummaryRedaction.Addresses).
const (
	addressesPlain   = "plain"
	addressesHashed  = "hashed"
	addressesDropped = "dropped"
)

// sharedTags is the type the drift summary counts a change of tags that
// several resources get from one place, such as local.tags, under: the
// change has no type, as it is of that place (see report.DriftChange.Covers).
const sharedTags = "shared_tags"

// outcomeCounts count changes by what became of them, as drift.json's
// totals do.
type outcomeCounts struct {
	Written         int `json:"written"`
	Kept            int `json:"kept"`
	RejectedByPlan  int `json:"rejected_by_plan"`
	RejectedByCheck int `json:"rejected_by_check"`
	Manual          int `json:"manual"`
	Secret          int `json:"secret"`
	NotAssessable   int `json:"not_assessable"`
	Absorbed        int `json:"absorbed"`
}

// add counts a change with outcome; it reports false for one that isn't a
// known outcome.
func (c *outcomeCounts) add(outcome string) bool {
	switch outcome {
	case report.DriftWritten:
		c.Written++
	case report.DriftKept:
		c.Kept++
	case report.DriftRejectedByPlan:
		c.RejectedByPlan++
	case report.DriftRejectedByCheck:
		c.RejectedByCheck++
	case report.DriftManual:
		c.Manual++
	case report.DriftSecret:
		c.Secret++
	case report.DriftNotAssessable:
		c.NotAssessable++
	case report.DriftAbsorbed:
		c.Absorbed++
	default:
		return false
	}
	return true
}

type driftSummaryTotals struct {
	Roots           int           `json:"roots"`
	RootsNotChecked int           `json:"roots_not_checked"`
	Changes         int           `json:"changes"`
	Outcomes        outcomeCounts `json:"outcomes"`
}

type driftTypeCounts struct {
	Type     string        `json:"type"`
	Changes  int           `json:"changes"`
	Outcomes outcomeCounts `json:"outcomes"`
}

type driftRootSummary struct {
	Root        string            `json:"root,omitempty"`
	Environment string            `json:"environment,omitempty"`
	Checked     bool              `json:"checked"`
	Changes     int               `json:"changes"`
	Outcomes    outcomeCounts     `json:"outcomes"`
	Types       []driftTypeCounts `json:"types"`
	// Addresses are the resources that changed, as drift.json names them,
	// hashed with --hash-ids, and left out with --drop-addresses.
	Addresses []string `json:"addresses,omitempty"`
}

type driftSummaryRedaction struct {
	Addresses string `json:"addresses"`
	HashAlg   string `json:"hash_alg"`
}

type driftSummaryDoc struct {
	Schema      string                `json:"schema"`
	RunID       string                `json:"run_id"`
	GeneratedAt string                `json:"generated_at"`
	DriftFound  bool                  `json:"drift_found"`
	Totals      driftSummaryTotals    `json:"totals"`
	Roots       []driftRootSummary    `json:"roots"`
	Redaction   driftSummaryRedaction `json:"redaction"`
}

// rootKey is a root as drift.json names it: its path and, for a root
// environments share, the environment it was checked as.
type rootKey struct{ path, environment string }

// buildDriftSummary counts the changes of d, which drift.json holds, by
// root, environment, outcome and type, and lists the resources that
// changed. It reads no values.
func buildDriftSummary(d *driftRead, opts Options, runID string, now time.Time) (*driftSummaryDoc, error) {
	red := newRedactor(nil, false, opts.HashKey)
	doc := &driftSummaryDoc{
		Schema:      schemaDriftSummary,
		RunID:       runID,
		GeneratedAt: formatTime(now),
		DriftFound:  d.DriftFound,
		Roots:       []driftRootSummary{},
		Redaction:   driftSummaryRedaction{Addresses: addressesPlain, HashAlg: hashAlg},
	}
	switch {
	case opts.DropAddresses:
		doc.Redaction.Addresses = addressesDropped
	case opts.HashKey != nil:
		doc.Redaction.Addresses = addressesHashed
	}

	type rootState struct {
		summary   *driftRootSummary
		types     map[string]*driftTypeCounts
		addresses map[string]bool
	}
	roots := map[rootKey]*rootState{}
	var order []rootKey
	var bad error
	root := func(k rootKey) *rootState {
		if s, ok := roots[k]; ok {
			return s
		}
		// A root's path is a path of the repository, which the addresses'
		// redaction covers: hashed with them, and left out with them.
		if secret.CredentialFormat(k.path) != "" || secret.CredentialFormat(k.environment) != "" {
			bad = errors.New("the path or environment of a root has a format of a credential: nothing is written")
		}
		path := ""
		if !opts.DropAddresses {
			path = red.hash(k.path)
		}
		s := &rootState{
			summary:   &driftRootSummary{Root: path, Environment: k.environment, Checked: true},
			types:     map[string]*driftTypeCounts{},
			addresses: map[string]bool{},
		}
		roots[k] = s
		order = append(order, k)
		return s
	}
	for _, r := range d.Roots {
		s := root(rootKey{r.Path, r.Environment})
		s.summary.Checked = r.Error == ""
		if !s.summary.Checked {
			doc.Totals.RootsNotChecked++
		}
	}
	// A change of a root the report doesn't list is of a root that was
	// checked.
	for _, c := range d.Changes {
		s := root(rootKey{c.Root, c.Environment})
		if !s.summary.Outcomes.add(c.Outcome) {
			return nil, fmt.Errorf("%s: a change has the outcome %q, which the drift summary doesn't know", report.DriftJSONFile, c.Outcome)
		}
		s.summary.Changes++
		doc.Totals.Outcomes.add(c.Outcome)
		doc.Totals.Changes++

		typ := c.Type
		if typ == "" {
			typ = sharedTags
		}
		t := s.types[typ]
		if t == nil {
			t = &driftTypeCounts{Type: typ}
			s.types[typ] = t
		}
		t.Changes++
		t.Outcomes.add(c.Outcome)

		if opts.DropAddresses {
			continue
		}
		// A change of tags several resources share is of the place that
		// sets them, which isn't a resource: the resources are listed.
		addresses := c.Covers
		if len(addresses) == 0 {
			addresses = []string{c.Address}
		}
		for _, a := range addresses {
			if a == "" {
				continue
			}
			if secret.CredentialFormat(a) != "" {
				return nil, fmt.Errorf("the address of a %s has a format of a credential: nothing is written", typ)
			}
			s.addresses[red.hash(a)] = true
		}
	}

	if bad != nil {
		return nil, bad
	}
	doc.Totals.Roots = len(order)
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].path != order[j].path {
			return order[i].path < order[j].path
		}
		return order[i].environment < order[j].environment
	})
	for _, k := range order {
		s := roots[k]
		s.summary.Types = []driftTypeCounts{}
		for _, typ := range sortedKeys(s.types) {
			s.summary.Types = append(s.summary.Types, *s.types[typ])
		}
		s.summary.Addresses = sortedKeys(s.addresses)
		doc.Roots = append(doc.Roots, *s.summary)
	}
	return doc, nil
}

// sortedKeys returns the keys of m, sorted; nil for none.
func sortedKeys[V any](m map[string]V) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
