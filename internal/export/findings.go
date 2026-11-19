// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"errors"
	"fmt"
	"time"

	"github.com/IgnatG/infraharvest/internal/engine/secret"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/version"
)

// The OCSF findings are the checks of the verification gate that failed or
// were skipped, as events of OCSF 1.9.0's class Detection Finding, in the
// category Findings, with the iac extension's iac_source profile (see
// ocsf.go and schemas/ocsf/iac). There is one event for each check of each
// root: the gate document says whether a check passed, over all roots, and
// these say where. An event holds the check's name and its reason codes,
// from the closed list of gate.go, and nothing else a check says: a check's
// details are free text, with addresses and values in them, and never go
// into the export.

// What the events say of OCSF (schemas/ocsf-findings-v1.schema.json).
const (
	findingCategoryUID = 2
	findingClassUID    = 2004
	findingActivityID  = 1 // Create
	findingTypeUID     = findingClassUID*100 + findingActivityID
	findingStatusNew   = 1
	// findingRootType is the type of the resource of a finding: the root
	// module the check ran on.
	findingRootType = "iac_root"
)

// The severities of a finding, by OCSF's severity_id. A failed check
// stopped the root from passing the gate and wants action: Medium, as the
// change isn't merged yet, and High for secrets in the output. A skipped
// check checked nothing, such as a scanner that isn't installed: Low.
const (
	severityLow    = 2
	severityMedium = 3
	severityHigh   = 4
)

// severityNames are the captions of the severities the events use.
var severityNames = map[int]string{severityLow: "Low", severityMedium: "Medium", severityHigh: "High"}

// What a finding says of its check.
const (
	findingFailed = "failed"
	findingWarned = "warned"
)

// checkTitles are the names of the checks of the gate, by ID, as the
// gate's own checks are named: a title is one of these, never a name the
// report has.
var checkTitles = map[string]string{
	"G1": "G1 format",
	"G2": "G2 validate",
	"G3": "G3 plan",
	"G4": "G4 standards",
	"G5": "G5 scanners",
	"G6": "G6 secrets",
	"G7": "G7 determinism",
	"G8": "G8 moves",
}

type findingInfo struct {
	UID   string   `json:"uid"`
	Title string   `json:"title"`
	Types []string `json:"types"`
}

// findingResource is the root a check ran on.
type findingResource struct {
	Type string     `json:"type"`
	UID  string     `json:"uid,omitempty"`
	IaC  *iacSource `json:"iac,omitempty"`
}

type findingEvent struct {
	ActivityID   int               `json:"activity_id"`
	ActivityName string            `json:"activity_name"`
	CategoryName string            `json:"category_name"`
	CategoryUID  int               `json:"category_uid"`
	ClassName    string            `json:"class_name"`
	ClassUID     int               `json:"class_uid"`
	IsAlert      bool              `json:"is_alert"`
	Message      string            `json:"message"`
	Severity     string            `json:"severity"`
	SeverityID   int               `json:"severity_id"`
	Status       string            `json:"status"`
	StatusID     int               `json:"status_id"`
	Time         int64             `json:"time"`
	TypeUID      int               `json:"type_uid"`
	FindingInfo  findingInfo       `json:"finding_info"`
	Metadata     ocsfMetadata      `json:"metadata"`
	Cloud        *ocsfCloud        `json:"cloud,omitempty"`
	Resources    []findingResource `json:"resources,omitempty"`
}

// findingsRedaction says what the document hashes or leaves out. It has no
// tags: the events have none.
type findingsRedaction struct {
	IDsHashed        bool   `json:"ids_hashed"`
	HashAlg          string `json:"hash_alg"`
	AddressesDropped bool   `json:"addresses_dropped"`
}

type findingsDoc struct {
	Schema      string            `json:"schema"`
	RunID       string            `json:"run_id"`
	OCSFVersion string            `json:"ocsf_version"`
	Redaction   findingsRedaction `json:"redaction"`
	Events      []findingEvent    `json:"events"`
}

// findingRoot is one root the gate ran on, as the report or verify.json
// says: its path, the scope its resources were listed in and the checks it
// ran. written is set for the root an import generated, whose iac source
// says where its configuration is.
type findingRoot struct {
	path, scope string
	checks      []report.Check
	written     bool
}

// findingsContext is what every event of the document shares.
type findingsContext struct {
	opts     Options
	red      *redactor
	runID    string
	now      time.Time
	meta     ocsfMetadata
	tool     string
	provider string
}

// buildFindingsOfReport builds the OCSF findings of the checks of the
// directories and merged roots of an import's report.
func buildFindingsOfReport(r *report.Report, opts Options, runID string, now time.Time) (*findingsDoc, error) {
	var roots []findingRoot
	var scopes []string
	for _, d := range r.Directories {
		roots = append(roots, findingRoot{path: d.Path, scope: d.Scope, checks: d.Checks, written: true})
		if d.Scope != "" {
			scopes = append(scopes, d.Scope)
		}
	}
	for _, m := range r.Merges {
		roots = append(roots, findingRoot{path: m.Path, checks: m.Checks, written: true})
	}
	for _, s := range r.Scopes {
		scopes = append(scopes, s.Scope)
	}
	fc := newFindingsContext(opts, runID, now, toComponent(r.Tool).Version)
	fc.tool = toolOf(r.Engine)
	// A report that doesn't say which cloud it was of still has its
	// findings, without the cloud.
	if cloud, err := cloudOf(r, scopes); err == nil {
		fc.provider = ocsfProvider(cloud)
	}
	return fc.build(roots)
}

// buildFindingsOfVerify builds the OCSF findings of the checks of the roots
// verify.json lists. Its roots have no scope and no import: their findings
// have the path of the root, but no iac source, which says what an import
// did with a root.
func buildFindingsOfVerify(v verifyRead, opts Options, runID string, now time.Time) (*findingsDoc, error) {
	roots := make([]findingRoot, 0, len(v))
	for _, root := range v {
		roots = append(roots, findingRoot{path: root.Path, checks: root.Checks})
	}
	return newFindingsContext(opts, runID, now, version.Version).build(roots)
}

func newFindingsContext(opts Options, runID string, now time.Time, productVersion string) *findingsContext {
	return &findingsContext{
		opts: opts, red: newRedactor(nil, false, opts.HashKey), runID: runID, now: now,
		meta: ocsfMetadata{
			Version:        ocsfVersion,
			CorrelationUID: runID,
			Product:        ocsfProductInfo{Name: ocsfProduct, Version: productVersion},
			Profiles:       []string{"cloud", ocsfProfileIaC},
			Extensions:     []ocsfExtensionInfo{{Name: ocsfExtension, UID: ocsfExtensionUID, Version: ocsfExtensionVersion}},
		},
	}
}

// build returns the document of roots: an event for each check of each root
// that failed, or that every run of was skipped, in the order of the roots
// and then of the checks.
func (fc *findingsContext) build(roots []findingRoot) (*findingsDoc, error) {
	doc := &findingsDoc{
		Schema: schemaOCSFFindings, RunID: fc.runID, OCSFVersion: ocsfVersion, Events: []findingEvent{},
		Redaction: findingsRedaction{IDsHashed: fc.opts.HashKey != nil, HashAlg: hashAlg, AddressesDropped: fc.opts.DropAddresses},
	}
	for i, root := range roots {
		sums := checkSummaries{}
		sums.add(root.checks)
		var resources []findingResource
		for _, id := range sums.ids() {
			s := sums[id]
			outcome := findingFailed
			switch {
			case !s.passed():
			case s.wasSkipped():
				outcome = findingWarned
			default:
				continue
			}
			if resources == nil {
				res, err := fc.resourcesOf(root)
				if err != nil {
					return nil, err
				}
				resources = res
			}
			doc.Events = append(doc.Events, fc.event(root, i+1, id, outcome, s.reasonCodes(), resources))
		}
	}
	return doc, nil
}

// event is the finding of check id of the root that is the n-th of the
// document. Its uid is the run's, the check's and the root's place in the
// run, which are the same for the same results and say nothing of the root.
func (fc *findingsContext) event(root findingRoot, n int, id, outcome string, codes []string, resources []findingResource) findingEvent {
	title := checkTitles[id]
	severity := severityMedium
	switch {
	case outcome == findingWarned:
		severity = severityLow
	case failedReason[id] == reasonSecrets:
		severity = severityHigh
	}
	message := title + " " + outcome
	for i, code := range codes {
		sep := ", "
		if i == 0 {
			sep = ": "
		}
		message += sep + code
	}
	event := findingEvent{
		ActivityID: findingActivityID, ActivityName: "Create",
		CategoryName: "Findings", CategoryUID: findingCategoryUID,
		ClassName: "Detection Finding", ClassUID: findingClassUID,
		IsAlert: outcome == findingFailed, Message: message,
		Severity: severityNames[severity], SeverityID: severity,
		Status: "New", StatusID: findingStatusNew,
		Time: fc.now.UnixMilli(), TypeUID: findingTypeUID,
		FindingInfo: findingInfo{UID: fmt.Sprintf("%s:%s:%d", fc.runID, id, n), Title: title, Types: codes},
		Metadata:    fc.meta,
		Resources:   resources,
	}
	if root.written && fc.provider != "" {
		event.Cloud = &ocsfCloud{Provider: fc.provider}
		account, region := scopeParts(root.scope)
		if account != "" {
			event.Cloud.Account, event.Cloud.Region = &ocsfAccount{UID: account}, region
		}
	}
	return event
}

// resourcesOf returns the root a finding is of, as a resource: its path as
// the uid, and, for a root an import generated, its iac source, with the
// outcome Written. With --drop-addresses it has no path, no state, and so
// no resource at all if there is nothing else to say; --hash-ids hashes
// them. A path or a state that looks like a credential stops the export, as
// it does for the OCSF inventory.
func (fc *findingsContext) resourcesOf(root findingRoot) ([]findingResource, error) {
	res := findingResource{Type: findingRootType}
	if root.written {
		res.IaC = &iacSource{
			Tool: fc.tool, RepoURL: repoURL(fc.opts.RepoURL), CommitSHA: fc.opts.CommitSHA,
			OutcomeID: importOutcomes[importWritten], Outcome: importWritten,
		}
	}
	if !fc.opts.DropAddresses {
		if secret.CredentialFormat(root.path) != "" {
			return nil, errors.New("the path of a root has a format of a credential: nothing is written")
		}
		res.UID = fc.red.hash(root.path)
		if res.IaC != nil {
			res.IaC.Path = res.UID
			if fc.opts.StateURL != nil {
				if state := fc.opts.StateURL(root.path); state != "" {
					if secret.CredentialFormat(state) != "" {
						return nil, errors.New("the state of a root has a format of a credential: nothing is written")
					}
					res.IaC.StateKey = fc.red.hash(state)
				}
			}
		}
	}
	if res.UID == "" && res.IaC == nil {
		return nil, nil
	}
	return []findingResource{res}, nil
}
