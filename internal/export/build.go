// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package export writes the metadata of an import that a hosted control
// plane may receive: the inventory of the estate, the run and the
// verification gate, the summary of a drift check, and the inventory as OCSF
// events, each a JSON document with a schema (see schemas/).
// It reads the report and the saved inventory of an output directory, or
// its drift report,
// copies into each document only the fields the document names, drops tags
// that look like secrets, hashes IDs and names if asked, checks the
// document against its schema, and returns it. It uploads nothing and makes
// no network calls.
package export

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	// StartedAt and FinishedAt are when the import ran, if the caller knows
	// them: the report holds no times, as its bytes name the run. The run
	// document has them when set, and the inventory's generated_at is
	// FinishedAt, so that exporting the same run again says the same time.
	StartedAt, FinishedAt time.Time
	// Now is the time generated_at has without FinishedAt; time.Now if nil.
	Now func() time.Time
	// Command is the command whose run the run document describes: one of
	// Commands. Without it, the command whose report the output directory
	// has (see commandOf).
	Command string
	// RequestID, if set, is the run document's request_id, which a control
	// plane gave to ask for the run.
	RequestID string
	// DropAddresses leaves the resource addresses, module addresses and
	// paths out of the documents that have them (the drift summary and the
	// OCSF inventory); HashKey hashes them otherwise.
	DropAddresses bool
	// RepoURL and CommitSHA are the repository and the commit the
	// configuration was generated into, for the OCSF inventory's iac
	// source: an https URL without credentials, and the hexadecimal ID of
	// a commit. Both are optional, and no other document has them.
	RepoURL, CommitSHA string
}

// Build returns the document opts asks for, as indented JSON, checked
// against its schema. It fails, and returns nothing, if the report can't be
// read (drift.json, for the drift summary and the run of a drift check), the
// output has something the schema doesn't allow, or the key is missing.
func Build(opts Options) ([]byte, error) {
	if !slices.Contains(Documents, opts.Document) {
		return nil, fmt.Errorf("unknown document %q: want one of %s", opts.Document, strings.Join(Documents, ", "))
	}
	if err := checkOptions(opts); err != nil {
		return nil, err
	}
	var (
		doc any
		err error
	)
	if commandOf(opts) == CommandDrift {
		doc, err = buildFromDrift(opts)
	} else {
		doc, err = buildFromReport(opts)
	}
	if err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := validateDocument(opts.Document, doc); err != nil {
		return nil, fmt.Errorf("the %s document doesn't match its schema, so nothing is written: %w", opts.Document, err)
	}
	return append(encoded, '\n'), nil
}

// requestIDRE is requestIDPattern, compiled.
var requestIDRE = regexp.MustCompile(requestIDPattern)

// checkOptions refuses options that can't be right, before anything is
// read.
func checkOptions(opts Options) error {
	if !opts.StartedAt.IsZero() && !opts.FinishedAt.IsZero() && opts.FinishedAt.Before(opts.StartedAt) {
		return fmt.Errorf("the run finished (%s) before it started (%s)", formatTime(opts.FinishedAt), formatTime(opts.StartedAt))
	}
	if opts.RequestID != "" && !requestIDRE.MatchString(opts.RequestID) {
		return fmt.Errorf("the request ID %q must be 1 to 128 letters, digits, dots, underscores, colons or hyphens", opts.RequestID)
	}
	if opts.RequestID != "" && opts.Document != DocRun {
		return fmt.Errorf("a request ID goes into the run document only, not the %s document", opts.Document)
	}
	if opts.Command != "" && !slices.Contains(Commands, opts.Command) {
		return fmt.Errorf("unknown command %q: want one of %s", opts.Command, strings.Join(Commands, ", "))
	}
	if opts.Command != "" && opts.Document != DocRun {
		return fmt.Errorf("a command is for the run document only: the %s document has no choice of command", opts.Document)
	}
	return checkRepo(opts)
}

// commandOf is the command whose run opts exports. Only the run document
// has a choice: opts.Command, else the command whose report the output
// directory has, which is import when it has the reports of both, as an
// import and a later drift leave. Neither, and the import's report is the
// one that fails to read. The drift summary is of a drift run.
func commandOf(opts Options) string {
	if opts.Document == DocDriftSummary {
		return CommandDrift
	}
	if opts.Document != DocRun || opts.Command != "" {
		return cmp.Or(opts.Command, CommandImport)
	}
	if exists(filepath.Join(opts.Out, report.Dir, reportFile)) || !exists(filepath.Join(opts.Out, report.Dir, report.DriftJSONFile)) {
		return CommandImport
	}
	return CommandDrift
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// reportFile is the report an import leaves in the report directory.
const reportFile = "report.json"

// buildFromReport builds the document of opts from the report of an
// import.
func buildFromReport(opts Options) (any, error) {
	// The report is read once: its bytes name the run, and its content
	// fills the document.
	content, err := os.ReadFile(filepath.Join(opts.Out, report.Dir, reportFile))
	if err != nil {
		return nil, err
	}
	r, err := report.Parse(content)
	if err != nil {
		return nil, err
	}
	runID := runID(content)
	switch opts.Document {
	case DocInventory:
		return buildInventory(r, opts, runID, generatedAt(opts))
	case DocGraph:
		return buildGraph(r, opts, runID, generatedAt(opts))
	case DocOCSFInventory:
		return buildOCSFInventory(r, opts, runID, generatedAt(opts))
	case DocRun:
		doc := buildRun(r, runID, opts.StartedAt, opts.FinishedAt)
		doc.RequestID = opts.RequestID
		return doc, nil
	}
	return buildGate(r, runID), nil
}

// buildFromDrift builds the document of opts from the drift report.
func buildFromDrift(opts Options) (any, error) {
	d, content, err := readDrift(opts.Out)
	if err != nil {
		return nil, err
	}
	runID := runID(content)
	if opts.Document == DocDriftSummary {
		return buildDriftSummary(d, opts, runID, generatedAt(opts))
	}
	doc := buildDriftRun(d, runID, opts.StartedAt, opts.FinishedAt)
	doc.RequestID = opts.RequestID
	return doc, nil
}

// generatedAt is the time a document's generated_at says: when the import
// finished, if the caller knows, so that exporting the same run again says
// the same time, else now.
func generatedAt(opts Options) time.Time {
	if !opts.FinishedAt.IsZero() {
		return opts.FinishedAt
	}
	if opts.Now != nil {
		return opts.Now()
	}
	return time.Now()
}

// timeLayout is the form of every timestamp in the documents: UTC, to the
// second (see the schemas).
const timeLayout = "2006-01-02T15:04:05Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// ParseTime reads the time the flag called name has: RFC 3339, such as
// 2026-11-03T09:30:00Z. Another offset is converted to UTC.
func ParseTime(name, value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s %q isn't an RFC 3339 time, such as 2026-11-03T09:30:00Z", name, value)
	}
	return t.UTC(), nil
}

// runID names the run, as a version 5 style UUID of content, its
// report.json: the same report has the same ID in all three documents, and
// in every export of it.
func runID(content []byte) string {
	sum := sha256.Sum256(content)
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
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
	// address and dir are where the import wrote the resource's
	// configuration: its address and the directory of its root, relative
	// to the output directory. Empty for a resource it didn't import.
	address, dir string
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
			e := add(d.Scope, engine.AddressType(res.Address), res.ID, "")
			if e.address == "" {
				e.address, e.dir = res.Address, d.Path
			}
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
		GeneratedAt: formatTime(now),
		Tool:        toComponent(r.Tool),
		Scopes:      []string{},
		Resources:   []resource{},
		Coverage:    coverage{Totals: toCounts(r.Totals), Types: []typeCounts{}, Scopes: []scopeCounts{}},
	}
	for _, t := range r.Types {
		doc.Coverage.Types = append(doc.Coverage.Types, typeCounts{Type: t.Type, Counts: toCounts(t.CoverageTotal)})
	}
	// scopes holds doc.Scopes, so that each resource's scope is looked up
	// in a set rather than searched for.
	scopes := map[string]bool{}
	for _, s := range r.Scopes {
		if s.Scope != "" {
			scopes[s.Scope] = true
			doc.Scopes = append(doc.Scopes, s.Scope)
			doc.Coverage.Scopes = append(doc.Coverage.Scopes, scopeCounts{Scope: s.Scope, Counts: toCounts(s.CoverageTotal)})
		}
	}
	for _, e := range entries(r, listed) {
		if e.scope != "" && !scopes[e.scope] {
			scopes[e.scope] = true
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
			Tags:        red.tags(e.tags, e.id, e.name),
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

func buildRun(r *report.Report, runID string, started, finished time.Time) *runDoc {
	doc := &runDoc{
		Schema:   schemaRun,
		RunID:    runID,
		Command:  CommandImport,
		ExitCode: r.ExitCode,
		Outcome:  outcomeOf(r.ExitCode),
		Tool:     toComponent(r.Tool),
		Engine:   toComponent(r.Engine),
		Coverage: toCounts(r.Totals),
	}
	if !started.IsZero() {
		doc.StartedAt = formatTime(started)
	}
	if !finished.IsZero() {
		doc.FinishedAt = formatTime(finished)
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
