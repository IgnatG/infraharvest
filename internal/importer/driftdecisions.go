// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"go.yaml.in/yaml/v3"

	"github.com/infraharvest/infraharvest-cli/internal/engine"
	"github.com/infraharvest/infraharvest-cli/internal/fsutil"
	"github.com/infraharvest/infraharvest-cli/internal/report"
	"github.com/infraharvest/infraharvest-cli/internal/selection"
)

// DriftDecisionsVersion is the decisions file format's version.
const DriftDecisionsVersion = 1

// DefaultDriftDecisionsFile is the decisions file --write-decisions
// writes in the output directory without --decisions.
const DefaultDriftDecisionsFile = "drift.yaml"

// DriftUndecided is the decision of a change nobody has decided yet: the
// file's rules and then its defaults decide it. --write-decisions makes
// an entry whose change changed again in the cloud undecided (see
// DriftDecisionEntry.Fingerprint); until then, the change is kept.
const DriftUndecided = "undecided"

// DriftDecisions is a decisions file: which changes infraharvest drift
// found to accept into the code, and which to keep. infraharvest drift
// --write-decisions writes one entry per change; people review and edit
// it, and drift --decisions applies it.
type DriftDecisions struct {
	Version int `yaml:"version"`
	// Defaults decide the changes neither Changes nor Rules decide.
	Defaults DriftDecisionDefaults `yaml:"defaults"`
	// Rules are evaluated in order; the last that matches decides.
	Rules []DriftDecisionRule `yaml:"rules,omitempty"`
	// Changes decide the changes they list, if their values are still
	// those they were decided for.
	Changes []DriftDecisionEntry `yaml:"changes"`

	// byKey indexes Changes by root, address and attribute (see
	// driftDecisionKey). It is built when the file is read or merged, and
	// only read while the roots are decided in parallel.
	byKey map[string]*DriftDecisionEntry
}

// DriftDecisionDefaults decide the changes nothing else decides.
type DriftDecisionDefaults struct {
	// Decision is accept or keep; "" keeps.
	Decision string `yaml:"decision"`
}

// DriftDecisionRule accepts or keeps the changes a match selects.
type DriftDecisionRule struct {
	Accept *DriftMatch `yaml:"accept,omitempty"`
	Keep   *DriftMatch `yaml:"keep,omitempty"`
}

// DriftMatch selects changes by attribute, type, address and root, each
// a list of patterns in which * matches any text, as --accept and --keep
// take them (see DriftRule): a change matches if every field given has a
// pattern it matches. A match with no field, written all, matches every
// change.
type DriftMatch struct {
	Attribute selection.Patterns `yaml:"attribute,omitempty"`
	Type      selection.Patterns `yaml:"type,omitempty"`
	Address   selection.Patterns `yaml:"address,omitempty"`
	Root      selection.Patterns `yaml:"root,omitempty"`
}

// UnmarshalYAML reads all, or a map of the fields, refusing others.
func (m *DriftMatch) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Value == "all" {
		*m = DriftMatch{}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: a rule matches all, or a map of attribute, type, address and root patterns", node.Line)
	}
	*m = DriftMatch{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		var patterns *selection.Patterns
		switch key.Value {
		case "attribute":
			patterns = &m.Attribute
		case "type":
			patterns = &m.Type
		case "address":
			patterns = &m.Address
		case "root":
			patterns = &m.Root
		default:
			return fmt.Errorf("line %d: a rule has no field %s: it matches attribute, type, address or root", key.Line, key.Value)
		}
		if err := value.Decode(patterns); err != nil {
			return err
		}
	}
	return nil
}

// driftMatchFields is a DriftMatch as written, without its methods.
type driftMatchFields DriftMatch

// MarshalYAML writes a match of every change as all.
func (m DriftMatch) MarshalYAML() (any, error) {
	if len(m.Attribute)+len(m.Type)+len(m.Address)+len(m.Root) == 0 {
		return "all", nil
	}
	return driftMatchFields(m), nil
}

// matches reports whether m matches c, a change of the root at root, as
// DriftRule does for each field (shared tags included).
func (m *DriftMatch) matches(root string, c engine.DriftChange) bool {
	for _, f := range []struct {
		field    string
		patterns selection.Patterns
	}{{"attribute", m.Attribute}, {"type", m.Type}, {"address", m.Address}, {"root", m.Root}} {
		if len(f.patterns) == 0 {
			continue
		}
		if !slices.ContainsFunc(f.patterns, func(p string) bool { return DriftRule{Field: f.field, Pattern: p}.matches(root, c) }) {
			return false
		}
	}
	return true
}

// DriftDecisionEntry is a change drift found and what to do about it.
type DriftDecisionEntry struct {
	// Root is the root's path in the output directory.
	Root    string `yaml:"root"`
	Address string `yaml:"address"`
	// Attribute is "" for a resource deleted in the cloud.
	Attribute string `yaml:"attribute,omitempty"`
	// Fingerprint is a hash of the change's values as the report shows
	// them, secrets redacted (see driftFingerprint): Decision holds only
	// while the change has those values.
	Fingerprint string `yaml:"fingerprint"`
	// Decision is accept, keep or undecided.
	Decision string `yaml:"decision"`
	// Note is for people; infraharvest keeps it.
	Note string `yaml:"note,omitempty"`
}

// LoadDriftDecisions reads and checks a decisions file.
func LoadDriftDecisions(path string) (*DriftDecisions, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseDriftDecisions(path, content)
}

// ParseDriftDecisions reads and checks content, a decisions file read
// from path, which its errors name.
func ParseDriftDecisions(path string, content []byte) (*DriftDecisions, error) {
	return parseDriftDecisions(path, content)
}

func parseDriftDecisions(path string, content []byte) (*DriftDecisions, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	f := &DriftDecisions{}
	if err := decoder.Decode(f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: empty: a decisions file starts with version: %d", path, DriftDecisionsVersion)
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Version != DriftDecisionsVersion {
		return nil, fmt.Errorf("%s: version %d isn't supported; this infraharvest reads version %d", path, f.Version, DriftDecisionsVersion)
	}
	switch f.Defaults.Decision {
	case "", engine.DriftAccept, engine.DriftKeep:
	default:
		return nil, fmt.Errorf("%s: defaults: decision must be accept or keep, not %q", path, f.Defaults.Decision)
	}
	for i, r := range f.Rules {
		if (r.Accept == nil) == (r.Keep == nil) {
			return nil, fmt.Errorf("%s: rule %d must have one of accept and keep", path, i+1)
		}
	}
	seen := map[string]bool{}
	for i, c := range f.Changes {
		switch {
		case c.Root == "" || c.Address == "":
			return nil, fmt.Errorf("%s: change %d needs a root and an address", path, i+1)
		case c.Fingerprint == "":
			return nil, fmt.Errorf("%s: change %d (%s %s) needs the fingerprint --write-decisions wrote for it", path, i+1, c.Address, c.Attribute)
		case c.Decision != engine.DriftAccept && c.Decision != engine.DriftKeep && c.Decision != DriftUndecided:
			return nil, fmt.Errorf("%s: change %d (%s %s): decision must be accept, keep or undecided, not %q", path, i+1, c.Address, c.Attribute, c.Decision)
		}
		k := driftDecisionKey(c.Root, c.Address, c.Attribute)
		if seen[k] {
			return nil, fmt.Errorf("%s: change %d (%s %s in %s) is listed twice", path, i+1, c.Address, c.Attribute, c.Root)
		}
		seen[k] = true
	}
	f.index()
	return f, nil
}

// driftDecisionKey identifies a change: the same address and attribute
// can change in several roots.
func driftDecisionKey(root, address, attribute string) string {
	return root + "\n" + address + "\n" + attribute
}

func (f *DriftDecisions) index() {
	f.byKey = make(map[string]*DriftDecisionEntry, len(f.Changes))
	for i := range f.Changes {
		c := &f.Changes[i]
		f.byKey[driftDecisionKey(c.Root, c.Address, c.Attribute)] = c
	}
}

// driftFingerprint is what a decision about a change is for: a SHA-256
// of its kind, the values before and after as the report shows them, in
// which every secret is already "(sensitive value)", and the resources a
// change of shared tags covers. It never sees a secret, so a change of
// only a secret has the same fingerprint whatever the secret becomes.
func driftFingerprint(kind string, before, after any, covers []string) string {
	if len(covers) == 0 {
		covers = nil
	}
	content, err := json.Marshal([]any{kind, before, after, covers})
	if err != nil {
		// Values read from a plan's JSON always marshal.
		content = fmt.Appendf(nil, "%#v", []any{kind, before, after, covers})
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// driftDecider decides the changes of a run: by the decisions file, if
// one is applied, then by the rules of --accept and --keep.
type driftDecider struct {
	file  *DriftDecisions
	rules []DriftRule
}

// active reports whether d decides anything: without a file or rules,
// every change is kept.
func (d driftDecider) active() bool {
	return d.file != nil || len(d.rules) > 0
}

// driftDecision is what a driftDecider decided for a change, where from,
// and whether the change's entry in the file was decided for other
// values.
type driftDecision struct {
	accept bool
	source string
	stale  bool
}

// decide decides c, a change of the root at root. Without a decisions
// file it is kept. With one, its defaults decide it, then its rules (the
// last that matches), then its entry, unless the entry is undecided,
// which leaves it to the rules and defaults. An entry whose fingerprint
// no longer matches the change keeps it, whatever the rules and defaults
// say. The rules of --accept and --keep come last, the last that matches
// deciding: on the command line, they decide over the file, a stale
// entry's too.
func (d driftDecider) decide(root string, c engine.DriftChange) driftDecision {
	out := driftDecision{source: report.DriftDecidedByDefault}
	if f := d.file; f != nil {
		out.accept, out.source = f.Defaults.Decision == engine.DriftAccept, report.DriftDecidedByDefaults
		for _, r := range f.Rules {
			switch {
			case r.Accept != nil && r.Accept.matches(root, c):
				out.accept, out.source = true, report.DriftDecidedByRule
			case r.Keep != nil && r.Keep.matches(root, c):
				out.accept, out.source = false, report.DriftDecidedByRule
			}
		}
		if e := f.byKey[driftDecisionKey(root, c.Address, c.Attribute)]; e != nil {
			switch {
			case e.Fingerprint != driftFingerprint(c.Kind, c.Before, c.After, c.Covers):
				// The entry was decided for other values: neither it nor
				// the file's rules and defaults decide what the change is
				// now, so it is kept until someone decides again.
				out.accept, out.source, out.stale = false, report.DriftDecidedByEntry, true
			case e.Decision != DriftUndecided:
				out.accept, out.source = e.Decision == engine.DriftAccept, report.DriftDecidedByEntry
			}
		}
	}
	for _, r := range d.rules {
		if r.matches(root, c) {
			out.accept, out.source = r.Accept, report.DriftDecidedByFlag
		}
	}
	return out
}

// decidable reports whether a change belongs in the decisions file: one
// the next apply would revert, whatever was decided for it. What the
// code absorbs, and what can't be assessed, has nothing to decide.
func decidable(outcome string) bool {
	return outcome != report.DriftAbsorbed && outcome != report.DriftNotAssessable
}

// Merge updates f with the changes of a run. An entry of a change found
// again keeps its decision and note if its fingerprint matches; if not,
// the change changed again in the cloud, and the entry becomes undecided
// with the new fingerprint. A change without an entry is added,
// undecided, so that the rules and defaults decide it until someone
// does. An entry of a root checked reports, whose change wasn't found
// again, as one accepted into the code or reverted by an apply, is
// dropped; entries of other roots, not checked or that failed, stay. It
// returns how many entries were added, became undecided and were
// dropped.
func (f *DriftDecisions) Merge(changes []report.DriftChange, checked func(root string) bool) (added, undecided, dropped int) {
	if f.byKey == nil {
		f.index()
	}
	seen := map[string]bool{}
	var merged []DriftDecisionEntry
	for _, c := range changes {
		if !decidable(c.Outcome) {
			continue
		}
		k := driftDecisionKey(c.Root, c.Address, c.Attribute)
		if seen[k] {
			continue
		}
		seen[k] = true
		fp := driftFingerprint(c.Kind, c.Before, c.After, c.Covers)
		if e, ok := f.byKey[k]; ok {
			m := *e
			if m.Fingerprint != fp {
				if m.Decision != DriftUndecided {
					undecided++
				}
				m.Fingerprint, m.Decision = fp, DriftUndecided
			}
			merged = append(merged, m)
			continue
		}
		merged = append(merged, DriftDecisionEntry{Root: c.Root, Address: c.Address, Attribute: c.Attribute, Fingerprint: fp, Decision: DriftUndecided})
		added++
	}
	for _, e := range f.Changes {
		if seen[driftDecisionKey(e.Root, e.Address, e.Attribute)] {
			continue
		}
		if checked != nil && !checked(e.Root) {
			merged = append(merged, e)
			continue
		}
		dropped++
	}
	f.Changes = merged
	f.index()
	return added, undecided, dropped
}

// driftDecisionsHeader explains a decisions file to the people who edit
// it.
const driftDecisionsHeader = `# infraharvest drift decisions: which changes in the cloud to take into the code.
#
# infraharvest drift --write-decisions wrote one entry per change the next
# apply would revert. Set decision to accept to write the cloud's value into
# the code, or to keep to keep the code's, which the next apply puts back.
# Then run:
#
#   infraharvest drift <output-directory> --decisions <this file>
#
# An undecided change follows the rules (the last matching one decides) and
# then the defaults (keep, unless they say accept). Rules match attribute,
# type, address and root with * as a wildcard; every field given must match,
# and all matches every change, for example:
#
#   rules:
#     - accept: { attribute: "tags.*" }
#     - keep: { type: aws_security_group }
#
# --accept and --keep on the command line decide after this file.
#
# A decision holds only for the values it was made for: the fingerprint is a
# hash of the change's values as the report shows them, secrets redacted; the
# values themselves aren't kept here. A change that changed again in the
# cloud is kept, whatever the rules say, and undecided again once this file
# is written again. Running --write-decisions again keeps the
# decisions and notes of entries whose fingerprint matches, adds new changes
# as undecided, and drops the changes no longer found in the roots it
# checked. Comments other than this header aren't kept: use note.
#
`

// Save writes the file with its changes sorted by root, address and
// attribute.
func (f *DriftDecisions) Save(path string) error {
	sort.SliceStable(f.Changes, func(i, j int) bool {
		a, b := f.Changes[i], f.Changes[j]
		if a.Root != b.Root {
			return a.Root < b.Root
		}
		if a.Address != b.Address {
			return a.Address < b.Address
		}
		return a.Attribute < b.Attribute
	})
	f.index()
	if f.Changes == nil {
		f.Changes = []DriftDecisionEntry{}
	}
	var out bytes.Buffer
	out.WriteString(driftDecisionsHeader)
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(f); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return fsutil.WriteFile(path, out.Bytes(), 0o644)
}

// newDriftDecisions is the decisions file --write-decisions starts from
// when there is none: every change kept unless decided otherwise.
func newDriftDecisions() *DriftDecisions {
	f := &DriftDecisions{Version: DriftDecisionsVersion, Defaults: DriftDecisionDefaults{Decision: engine.DriftKeep}}
	f.index()
	return f
}

// decidedDrift holds the decisions taken for a root's changes, by
// address and attribute, and, for a change of shared tags, by those of
// the resources it covers, whose changes take its decision when its edit
// is left out.
type decidedDrift struct {
	own, covered map[string]driftDecision
}

func newDecidedDrift() decidedDrift {
	return decidedDrift{own: map[string]driftDecision{}, covered: map[string]driftDecision{}}
}

// record records d, decided for c.
func (r decidedDrift) record(c engine.DriftChange, d driftDecision) {
	r.own[driftDecisionKey("", c.Address, c.Attribute)] = d
	for _, a := range c.Covers {
		r.covered[driftDecisionKey("", a, c.Attribute)] = d
	}
}

// of returns the decision taken for c: its own, else that of the change
// of shared tags that covered it, else, for a change nothing decided,
// what decide says.
func (r decidedDrift) of(c engine.DriftChange, decide func() driftDecision) driftDecision {
	k := driftDecisionKey("", c.Address, c.Attribute)
	if d, ok := r.own[k]; ok {
		return d
	}
	if d, ok := r.covered[k]; ok {
		return d
	}
	return decide()
}

// driftDecisionsPath is the decisions file --write-decisions writes:
// --decisions, else DefaultDriftDecisionsFile in the output directory.
func driftDecisionsPath(options DriftOptions) string {
	if options.Decisions != "" {
		return options.Decisions
	}
	return filepath.Join(options.Out, DefaultDriftDecisionsFile)
}

// decisionsToMerge returns the decisions file --write-decisions merges
// the run's changes into: applied, the one --decisions read; else the
// one already at its path, or, if there is none, a new one.
func decisionsToMerge(options DriftOptions, applied *DriftDecisions) (*DriftDecisions, error) {
	if applied != nil {
		return applied, nil
	}
	path := driftDecisionsPath(options)
	f, err := LoadDriftDecisions(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return newDriftDecisions(), nil
	case err != nil:
		return nil, fmt.Errorf("--write-decisions: %w", err)
	}
	return f, nil
}

// writeDriftDecisions merges the changes of d into f and writes it to
// path, and returns a line for the summary that says so. Entries of the
// roots d couldn't check stay as they are.
func writeDriftDecisions(path string, f *DriftDecisions, d *report.Drift) (string, error) {
	checked := map[string]bool{}
	for _, r := range d.Roots {
		checked[r.Path] = r.Error == ""
	}
	added, undecided, dropped := f.Merge(d.Changes, func(root string) bool { return checked[root] })
	if err := f.Save(path); err != nil {
		return "", fmt.Errorf("--write-decisions: %w", err)
	}
	return fmt.Sprintf("decisions: %s (%d added, %d undecided again, %d dropped)", path, added, undecided, dropped), nil
}
