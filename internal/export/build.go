// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package export writes the metadata of an import that a hosted control
// plane may receive: the inventory of the estate, the run and the
// verification gate, each a JSON document with a schema (see schemas/).
// It reads the report and the saved inventory of an output directory,
// copies into each document only the fields the document names, drops tags
// that look like secrets, hashes IDs and names if asked, checks the
// document against its schema, and returns it. It uploads nothing and makes
// no network calls.
package export

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/report"
)

// Options say what to export.
type Options struct {
	// Out is the output directory of the import.
	Out string
	// Document is the document to write: one of Documents.
	Document string
	// DropTags are tag keys to leave out; DropAllTags leaves out all tags.
	DropTags    []string
	DropAllTags bool
	// HashKey, if set, hashes IDs and names (see ReadKey).
	HashKey []byte
	// Now is the time generated_at has; time.Now if nil.
	Now func() time.Time
}

// Build returns the document opts asks for, as indented JSON, checked
// against its schema. It fails, and returns nothing, if the report can't be
// read, the output has something the schema doesn't allow, or the key is
// missing.
func Build(opts Options) ([]byte, error) {
	if !slices.Contains(Documents, opts.Document) {
		return nil, fmt.Errorf("unknown document %q: want one of %s", opts.Document, strings.Join(Documents, ", "))
	}
	r, err := report.Read(opts.Out)
	if err != nil {
		return nil, err
	}
	runID, err := runID(opts.Out)
	if err != nil {
		return nil, err
	}
	var doc any
	switch opts.Document {
	case DocInventory:
		now := time.Now
		if opts.Now != nil {
			now = opts.Now
		}
		doc, err = buildInventory(r, opts, runID, now().UTC())
	case DocRun:
		doc = buildRun(r, runID)
	case DocGate:
		doc = buildGate(r, runID)
	}
	if err != nil {
		return nil, err
	}
	content, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := validate(opts.Document, content); err != nil {
		return nil, fmt.Errorf("the %s document doesn't match its schema, so nothing is written: %w", opts.Document, err)
	}
	return append(content, '\n'), nil
}

// runID names the run, as a version 5 style UUID of its report.json: the
// same report has the same ID in all three documents, and in every export
// of it.
func runID(out string) (string, error) {
	content, err := os.ReadFile(filepath.Join(out, report.Dir, "report.json"))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func toCounts(c report.CoverageTotal) counts {
	return counts{
		Discovered: c.Discovered, Imported: c.Imported, LeftOut: c.LeftOut, Skipped: c.Skipped,
		Excluded: c.Excluded, Failed: c.Failed, Managed: c.Managed, OtherTool: c.OtherTool, Cloud: c.Cloud,
	}
}

func toComponent(c report.Component) component {
	if c.Version == "" {
		c.Version = "unknown"
	}
	return component{Name: c.Name, Version: c.Version}
}

// cloudOf returns the cloud the import was of: aws, azure or google, as
// the provider the generated configuration requires, or the scopes, say.
func cloudOf(r *report.Report, scopes []string) (string, error) {
	switch source := r.Provider.Source; {
	case strings.HasSuffix(source, "/aws"):
		return "aws", nil
	case strings.HasSuffix(source, "/azurerm"), strings.HasSuffix(source, "/azapi"):
		return "azure", nil
	case strings.HasSuffix(source, "/google"), strings.HasSuffix(source, "/google-beta"):
		return "google", nil
	}
	for _, s := range scopes {
		if cloud, _, _ := strings.Cut(s, "/"); slices.Contains([]string{"aws", "azure", "google"}, cloud) {
			return cloud, nil
		}
	}
	return "", errors.New("the report doesn't say which cloud the import was of")
}

// scopeParts splits a scope, such as aws/123456789012/eu-west-2, into its
// account and region; "" for a scope that lacks them.
func scopeParts(scope string) (account, region string) {
	parts := strings.SplitN(scope, "/", 3)
	if len(parts) == 3 {
		return parts[1], parts[2]
	}
	return "", ""
}

// entry is a resource the export lists, as the report and the saved
// inventories tell of it.
type entry struct {
	scope, typ, id string
	name           string
	tags           map[string]string
	// owner is what manages it, one of the report package's Owner
	// constants, or "".
	owner string
}

type entryKey struct{ scope, typ, id string }

// entries gathers the resources the report names, which an import took
// over, left out or excluded, and those the saved inventories list besides
// them, which add their names and tags.
func entries(r *report.Report, listed []listedResource) []*entry {
	byKey := map[entryKey]*entry{}
	var order []*entry
	add := func(scope, typ, id, owner string) *entry {
		k := entryKey{scope, typ, id}
		if e, ok := byKey[k]; ok {
			return e
		}
		// A resource the report lists in a directory that has no scope is
		// the one the inventory lists in a scope.
		if e, ok := byKey[entryKey{"", typ, id}]; ok && scope != "" {
			delete(byKey, entryKey{"", typ, id})
			e.scope = scope
			byKey[k] = e
			return e
		}
		e := &entry{scope: scope, typ: typ, id: id, owner: owner}
		byKey[k] = e
		order = append(order, e)
		return e
	}
	for _, d := range r.Directories {
		for _, res := range d.Imported {
			add(d.Scope, engine.AddressType(res.Address), res.ID, "")
		}
		for _, res := range d.LeftOut {
			add(d.Scope, engine.AddressType(res.Address), res.ID, "")
		}
	}
	for _, x := range r.Excluded {
		add(x.Scope, x.Type, x.ID, x.ManagedBy())
	}
	for _, l := range listed {
		e := add(l.Scope, l.Type, l.ID, "")
		e.name, e.tags = l.Name, l.Tags
	}
	order = slices.DeleteFunc(order, func(e *entry) bool { return e.typ == "" || e.id == "" })
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if a.scope != b.scope {
			return a.scope < b.scope
		}
		if a.typ != b.typ {
			return a.typ < b.typ
		}
		return a.id < b.id
	})
	return order
}

func buildInventory(r *report.Report, opts Options, runID string, now time.Time) (*inventoryDoc, error) {
	listed, err := readInventories(opts.Out)
	if err != nil {
		return nil, err
	}
	red := newRedactor(opts.DropTags, opts.DropAllTags, opts.HashKey)
	doc := &inventoryDoc{
		Schema:      schemaInventory,
		RunID:       runID,
		GeneratedAt: now.Format("2006-01-02T15:04:05Z"),
		Tool:        toComponent(r.Tool),
		Scopes:      []string{},
		Resources:   []resource{},
		Coverage:    coverage{Totals: toCounts(r.Totals), Types: []typeCounts{}, Scopes: []scopeCounts{}},
	}
	for _, t := range r.Types {
		doc.Coverage.Types = append(doc.Coverage.Types, typeCounts{Type: t.Type, Counts: toCounts(t.CoverageTotal)})
	}
	for _, s := range r.Scopes {
		if s.Scope != "" {
			doc.Scopes = append(doc.Scopes, s.Scope)
			doc.Coverage.Scopes = append(doc.Coverage.Scopes, scopeCounts{Scope: s.Scope, Counts: toCounts(s.CoverageTotal)})
		}
	}
	for _, e := range entries(r, listed) {
		if e.scope != "" && !slices.Contains(doc.Scopes, e.scope) {
			doc.Scopes = append(doc.Scopes, e.scope)
		}
		if secret.CredentialFormat(e.id) != "" {
			return nil, fmt.Errorf("a %s has an ID that looks like a credential: nothing is written", e.typ)
		}
		account, region := scopeParts(e.scope)
		if account == "" {
			account = "unknown"
		}
		status, basis := report.StatusOf(e.owner)
		res := resource{
			Type:        e.typ,
			ID:          red.hash(e.id),
			Account:     account,
			Status:      status,
			StatusBasis: basis,
			Region:      region,
			Tags:        red.tags(e.tags),
		}
		if e.name != "" && secret.CredentialFormat(e.name) == "" {
			res.Name = red.hash(e.name)
		}
		if status != report.StatusUnmanaged || e.owner == report.OwnerCloud {
			res.Owner = e.owner
		}
		doc.Resources = append(doc.Resources, res)
	}
	sort.Strings(doc.Scopes)
	cloud, err := cloudOf(r, doc.Scopes)
	if err != nil {
		return nil, err
	}
	doc.Cloud = cloud
	doc.Redaction = redaction{TagKeysDropped: red.droppedKeys(), AllTagsDropped: opts.DropAllTags, IDsHashed: opts.HashKey != nil, HashAlg: hashAlg}
	return doc, nil
}

// Outcomes of a run.
const (
	outcomeOK          = "ok"
	outcomePartial     = "partial"
	outcomeFailed      = "failed"
	outcomeInterrupted = "interrupted"
)

// The failure codes of a run (see schemas/run-v1.schema.json).
const (
	failRun         = "run_failed"
	failLeftOut     = "resources_left_out"
	failFailed      = "resources_failed"
	failCheck       = "check_failed"
	failInterrupted = "interrupted"
)

// outcomeOf returns the outcome of a run that exited with code.
func outcomeOf(code int) string {
	switch code {
	case report.ExitOK:
		return outcomeOK
	case report.ExitPartial:
		return outcomePartial
	case report.ExitInterrupted, report.ExitTerminated:
		return outcomeInterrupted
	}
	return outcomeFailed
}

func buildRun(r *report.Report, runID string) *runDoc {
	doc := &runDoc{
		Schema:   schemaRun,
		RunID:    runID,
		Command:  "import",
		ExitCode: r.ExitCode,
		Outcome:  outcomeOf(r.ExitCode),
		Tool:     toComponent(r.Tool),
		Engine:   toComponent(r.Engine),
		Coverage: toCounts(r.Totals),
	}
	if len(r.Failures) > 0 {
		doc.FailureCodes = append(doc.FailureCodes, failRun)
	}
	if r.Totals.LeftOut > 0 {
		doc.FailureCodes = append(doc.FailureCodes, failLeftOut)
	}
	if r.Totals.Failed > 0 {
		doc.FailureCodes = append(doc.FailureCodes, failFailed)
	}
	if len(r.FailedChecks()) > 0 {
		doc.FailureCodes = append(doc.FailureCodes, failCheck)
	}
	if doc.Outcome == outcomeInterrupted {
		doc.FailureCodes = append(doc.FailureCodes, failInterrupted)
	}
	if doc.Outcome == outcomeFailed && len(doc.FailureCodes) == 0 {
		doc.FailureCodes = append(doc.FailureCodes, failRun)
	}
	return doc
}
