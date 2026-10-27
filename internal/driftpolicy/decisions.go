// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package driftpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"

	"go.yaml.in/yaml/v3"

	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/report"
)

// DecisionsVersion is the decisions file format's version.
const DecisionsVersion = 1

// DefaultDecisionsFile is the decisions file --write-decisions writes in
// the output directory without --decisions.
const DefaultDecisionsFile = "drift.yaml"

// Undecided is the decision of a change nobody has decided yet: the
// file's rules and then its defaults decide it. --write-decisions makes
// an entry whose change changed again in the cloud undecided (see
// Entry.Fingerprint); until then, the change is kept.
const Undecided = "undecided"

// Decisions is a decisions file: which changes infraharvest drift found
// to accept into the code, and which to keep. infraharvest drift
// --write-decisions writes one entry per change; people review and edit
// it, and drift --decisions applies it.
type Decisions struct {
	Version int `yaml:"version"`
	// Defaults decide the changes neither Changes nor Rules decide.
	Defaults Defaults `yaml:"defaults"`
	// Rules are evaluated in order; the last that matches decides.
	Rules []DecisionRule `yaml:"rules,omitempty"`
	// Changes decide the changes they list, if their values are still
	// those they were decided for.
	Changes []Entry `yaml:"changes"`

	// byKey indexes Changes by root, address and attribute (see
	// decisionKey). It is built when the file is read, made or merged,
	// and only read while the roots are decided in parallel.
	byKey map[string]*Entry
}

// Defaults decide the changes nothing else decides.
type Defaults struct {
	// Decision is accept or keep; "" keeps.
	Decision string `yaml:"decision"`
}

// DecisionRule accepts or keeps the changes a match selects.
type DecisionRule struct {
	Accept *Match `yaml:"accept,omitempty"`
	Keep   *Match `yaml:"keep,omitempty"`
}

// Match selects changes by attribute, type, address and root, each a
// list of patterns in which * matches any text, as --accept and --keep
// take them (see Rule): a change matches if every field given has a
// pattern it matches. A match with no field, written all, matches every
// change.
type Match struct {
	Attribute []string `yaml:"attribute,omitempty"`
	Type      []string `yaml:"type,omitempty"`
	Address   []string `yaml:"address,omitempty"`
	Root      []string `yaml:"root,omitempty"`
}

// UnmarshalYAML reads all, or a map of the fields, refusing others. A
// field is one pattern or a list of them.
func (m *Match) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Value == "all" {
		*m = Match{}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: a rule matches all, or a map of attribute, type, address and root patterns", node.Line)
	}
	*m = Match{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		var patterns *[]string
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
		if value.Kind == yaml.ScalarNode {
			*patterns = []string{value.Value}
			continue
		}
		if err := value.Decode(patterns); err != nil {
			return err
		}
	}
	return nil
}

// matchFields is a Match as written, without its methods.
type matchFields Match

// MarshalYAML writes a match of every change as all.
func (m Match) MarshalYAML() (any, error) {
	if len(m.Attribute)+len(m.Type)+len(m.Address)+len(m.Root) == 0 {
		return "all", nil
	}
	return matchFields(m), nil
}

// matches reports whether m matches c, as Rule does for each field
// (shared tags included).
func (m *Match) matches(c report.DriftChange) bool {
	for _, f := range []struct {
		field    string
		patterns []string
	}{{"attribute", m.Attribute}, {"type", m.Type}, {"address", m.Address}, {"root", m.Root}} {
		if len(f.patterns) == 0 {
			continue
		}
		if !slices.ContainsFunc(f.patterns, func(p string) bool { return Rule{Field: f.field, Pattern: p}.matches(c) }) {
			return false
		}
	}
	return true
}

// Entry is a change drift found and what to do about it.
type Entry struct {
	// Root is the root's path in the output directory.
	Root string `yaml:"root"`
	// Environment is, for a root environments share, the environment
	// the change is in.
	Environment string `yaml:"environment,omitempty"`
	Address     string `yaml:"address"`
	// Attribute is "" for a resource deleted in the cloud.
	Attribute string `yaml:"attribute,omitempty"`
	// Fingerprint is a hash of the change's values as the report shows
	// them, secrets redacted (see fingerprint): Decision holds only while
	// the change has those values.
	Fingerprint string `yaml:"fingerprint"`
	// Decision is accept, keep or undecided.
	Decision string `yaml:"decision"`
	// Note is for people; infraharvest keeps it.
	Note string `yaml:"note,omitempty"`
}

// NewDecisions is the decisions file --write-decisions starts from when
// there is none: every change kept unless decided otherwise.
func NewDecisions() *Decisions {
	f := &Decisions{Version: DecisionsVersion, Defaults: Defaults{Decision: report.DriftKeep}}
	f.index()
	return f
}

// LoadDecisions reads and checks a decisions file.
func LoadDecisions(path string) (*Decisions, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseDecisions(path, content)
}

// ParseDecisions reads and checks content, a decisions file read from
// path, which its errors name.
func ParseDecisions(path string, content []byte) (*Decisions, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	f := &Decisions{}
	if err := decoder.Decode(f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: empty: a decisions file starts with version: %d", path, DecisionsVersion)
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Version != DecisionsVersion {
		return nil, fmt.Errorf("%s: version %d isn't supported; this infraharvest reads version %d", path, f.Version, DecisionsVersion)
	}
	switch f.Defaults.Decision {
	case "", report.DriftAccept, report.DriftKeep:
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
		case c.Decision != report.DriftAccept && c.Decision != report.DriftKeep && c.Decision != Undecided:
			return nil, fmt.Errorf("%s: change %d (%s %s): decision must be accept, keep or undecided, not %q", path, i+1, c.Address, c.Attribute, c.Decision)
		}
		k := decisionKey(rootName(c.Root, c.Environment), c.Address, c.Attribute)
		if seen[k] {
			return nil, fmt.Errorf("%s: change %d (%s %s in %s) is listed twice", path, i+1, c.Address, c.Attribute, rootName(c.Root, c.Environment))
		}
		seen[k] = true
	}
	f.index()
	return f, nil
}

// decisionKey identifies a change: the same address and attribute can
// change in several roots, and in several environments of a root they
// share, which root then names (see rootName).
func decisionKey(root, address, attribute string) string {
	return root + "\n" + address + "\n" + attribute
}

func (f *Decisions) index() {
	f.byKey = make(map[string]*Entry, len(f.Changes))
	for i := range f.Changes {
		c := &f.Changes[i]
		f.byKey[decisionKey(rootName(c.Root, c.Environment), c.Address, c.Attribute)] = c
	}
}

// entry returns the entry of c, or nil.
func (f *Decisions) entry(c report.DriftChange) *Entry {
	return f.byKey[decisionKey(c.Name(), c.Address, c.Attribute)]
}

// fingerprint is what a decision about a change is for: a SHA-256 of
// its kind, the values before and after as the report shows them, in
// which every secret is already "(sensitive value)", and the resources a
// change of shared tags covers. It never sees a secret, so a change of
// only a secret has the same fingerprint whatever the secret becomes.
func fingerprint(c report.DriftChange) string {
	covers := c.Covers
	if len(covers) == 0 {
		covers = nil
	}
	content, err := json.Marshal([]any{c.Kind, c.Before, c.After, covers})
	if err != nil {
		// Values read from a plan's JSON always marshal.
		content = fmt.Appendf(nil, "%#v", []any{c.Kind, c.Before, c.After, covers})
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
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
func (f *Decisions) Merge(changes []report.DriftChange, checked func(root string) bool) (added, undecided, dropped int) {
	if f.byKey == nil {
		f.index()
	}
	seen := map[string]bool{}
	var merged []Entry
	for _, c := range changes {
		if !decidable(c.Outcome) {
			continue
		}
		k := decisionKey(c.Name(), c.Address, c.Attribute)
		if seen[k] {
			continue
		}
		seen[k] = true
		fp := fingerprint(c)
		if e, ok := f.byKey[k]; ok {
			m := *e
			if m.Fingerprint != fp {
				if m.Decision != Undecided {
					undecided++
				}
				m.Fingerprint, m.Decision = fp, Undecided
			}
			merged = append(merged, m)
			continue
		}
		merged = append(merged, Entry{Root: c.Root, Environment: c.Environment, Address: c.Address, Attribute: c.Attribute, Fingerprint: fp, Decision: Undecided})
		added++
	}
	for _, e := range f.Changes {
		if seen[decisionKey(rootName(e.Root, e.Environment), e.Address, e.Attribute)] {
			continue
		}
		if checked != nil && !checked(rootName(e.Root, e.Environment)) {
			merged = append(merged, e)
			continue
		}
		dropped++
	}
	f.Changes = merged
	f.index()
	return added, undecided, dropped
}

// decisionsHeader explains a decisions file to the people who edit it.
const decisionsHeader = `# infraharvest drift decisions: which changes in the cloud to take into the code.
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
func (f *Decisions) Save(path string) error {
	sort.SliceStable(f.Changes, func(i, j int) bool {
		a, b := f.Changes[i], f.Changes[j]
		if a.Root != b.Root {
			return a.Root < b.Root
		}
		if a.Environment != b.Environment {
			return a.Environment < b.Environment
		}
		if a.Address != b.Address {
			return a.Address < b.Address
		}
		return a.Attribute < b.Attribute
	})
	f.index()
	if f.Changes == nil {
		f.Changes = []Entry{}
	}
	var out bytes.Buffer
	out.WriteString(decisionsHeader)
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

// rootName names the root at path, as environment env for a root
// environments share (see report.DriftRoot.Name).
func rootName(path, env string) string {
	return report.DriftRoot{Path: path, Environment: env}.Name()
}
