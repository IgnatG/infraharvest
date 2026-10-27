// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package selection decides which listed resources an import brings under
// Terraform. infraharvest discover writes a selection file with every
// resource it finds, each marked included or not by the default rules;
// people review and edit it, and import --selection imports what it
// includes.
package selection

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/IgnatG/infraharvest/internal/fsutil"
	"github.com/IgnatG/infraharvest/internal/managed"
	"github.com/IgnatG/infraharvest/internal/report"
)

// Version is the selection file format's version.
const Version = 1

// File is a selection file.
type File struct {
	Version int `yaml:"version"`
	// Defaults decide resources that neither Resources nor Rules decide,
	// such as resources created since discover ran.
	Defaults Defaults `yaml:"defaults"`
	// Rules are evaluated in order; the last that matches decides.
	Rules []Rule `yaml:"rules,omitempty"`
	// Resources decide the resources they list.
	Resources []Resource `yaml:"resources"`
	// IncludeOwned lets rules include what another Terraform state or tool
	// manages (see owned), as --include-owned does. It isn't saved.
	IncludeOwned bool `yaml:"-"`
	// Refresh says which of the reasons infraharvest gave its entries the
	// discover that Merge takes the listing of found again in full, so that
	// Merge may change them (see Merge). It isn't saved.
	Refresh Refresh `yaml:"-"`

	// byKey indexes Resources by scope, type and ID (see key); anyScope by
	// type and ID only, for lookups that don't know the scope. indexMu
	// guards building them: the accounts of one import decide in parallel.
	// Merge is not safe for concurrent use.
	indexMu  sync.Mutex
	byKey    map[string]*Resource
	anyScope map[string]*Resource
}

// Refresh says which kinds of reason a discover worked out again in full
// for what it listed: a reason of a kind it did is up to date in the
// listing, which has none where the resource no longer has one.
type Refresh struct {
	// State: what the states of --managed-state have (see
	// report.OwnerTerraformState).
	State bool
	// Tags: what the tags and labels say manages a resource (see
	// report.ByTag); not with --include-owned, which doesn't look.
	Tags bool
	// Defaults: the provider's default selection, such as what the cloud
	// manages itself or what CloudFormation stacks list.
	Defaults bool
}

// covers reports whether r says reason, of an entry, is up to date in the
// listing.
func (r Refresh) covers(reason string) bool {
	switch {
	case report.ByTag(reason):
		return r.Tags
	case report.OwnerOf(reason) == report.OwnerTerraformState:
		return r.State
	}
	return r.Defaults
}

// Defaults decide resources nothing else decides.
type Defaults struct {
	Include bool `yaml:"include"`
}

// Rule includes or excludes the resources a match selects.
type Rule struct {
	Include *Match `yaml:"include,omitempty"`
	Exclude *Match `yaml:"exclude,omitempty"`
}

// Match selects resources by type, ID, name and tags, each a list of
// patterns in which * matches any text. A resource matches if every field
// given has a pattern it matches. Tags match by key: a resource without
// the tag has the value "", so { owner: "" } matches untagged resources.
type Match struct {
	Type Patterns            `yaml:"type,omitempty"`
	ID   Patterns            `yaml:"id,omitempty"`
	Name Patterns            `yaml:"name,omitempty"`
	Tags map[string]Patterns `yaml:"tags,omitempty"`
}

// Patterns are glob patterns, written as one string or a list.
type Patterns []string

// UnmarshalYAML accepts a single pattern as well as a list.
func (p *Patterns) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*p = Patterns{node.Value}
		return nil
	}
	var list []string
	if err := node.Decode(&list); err != nil {
		return err
	}
	*p = list
	return nil
}

// Resource is a listed resource and whether to import it.
type Resource struct {
	Type string `yaml:"type"`
	ID   string `yaml:"id"`
	Name string `yaml:"name,omitempty"`
	// Scope is where discover listed it: provider, account and region, as
	// the root it is imported into is laid out (aws/123456789012/eu-west-2).
	Scope string `yaml:"scope,omitempty"`
	// Service is the service that listed it, so that a discover that
	// lists other services leaves the entry as it is (see Merge).
	Service string `yaml:"service,omitempty"`
	// Tags are the resource's tags (labels on Google Cloud) when discover
	// could read them, for rules and the picker.
	Tags    map[string]string `yaml:"tags,omitempty"`
	Include bool              `yaml:"include"`
	// Reason says why the default rules exclude the resource.
	Reason string `yaml:"reason,omitempty"`
	// Note is for people; infraharvest keeps it.
	Note string `yaml:"note,omitempty"`
	// New marks a resource discover found after the file was first written.
	// Remove the mark once the entry is reviewed.
	New bool `yaml:"new,omitempty"`
}

// Decision is whether to import a resource, and why not.
type Decision struct {
	Include bool
	Reason  string
}

// Decide returns the decision for the untagged resource of resourceType
// with id and name, whatever scope it is listed in (see DecideIn).
func (f *File) Decide(resourceType, id, name string) Decision {
	return f.DecideIn("", resourceType, id, name, nil)
}

// DecideIn returns the decision for the resource of resourceType with id,
// name and tags in scope: its entry in Resources if it has one (see
// HasIn), else the last rule that matches it, else Defaults.
func (f *File) DecideIn(scope, resourceType, id, name string, tags map[string]string) Decision {
	if r, ok := f.lookup(scope, resourceType, id); ok {
		if r.Include {
			return Decision{Include: true}
		}
		reason := r.Reason
		if reason == "" {
			reason = report.ReasonSelectionEntry
		}
		return Decision{Reason: reason}
	}
	if d, ok := f.byRule(resourceType, id, name, tags); ok {
		return d
	}
	if f.Defaults.Include {
		return Decision{Include: true}
	}
	return Decision{Reason: report.ReasonSelectionDefaults}
}

// mayInclude reports whether f may include a resource of resourceType
// whatever the default selection says: an entry of the type includes one,
// or a rule includes resources of the type, or of any type. It looks only
// at types, so it may say a type is included that no resource of it is.
func (f *File) mayInclude(resourceType string) bool {
	for _, r := range f.Resources {
		if r.Include && r.Type == resourceType {
			return true
		}
	}
	for _, rule := range f.Rules {
		if rule.Include != nil && rule.Include.Type.match(resourceType) {
			return true
		}
	}
	return false
}

// byRule returns the decision of the last rule that matches the resource
// of resourceType with id, name and tags, and whether one does.
func (f *File) byRule(resourceType, id, name string, tags map[string]string) (Decision, bool) {
	var decision Decision
	matched := false
	for _, rule := range f.Rules {
		if rule.Include != nil && rule.Include.matches(resourceType, id, name, tags) {
			decision, matched = Decision{Include: true}, true
		}
		if rule.Exclude != nil && rule.Exclude.matches(resourceType, id, name, tags) {
			decision, matched = Decision{Reason: report.ReasonSelectionRule}, true
		}
	}
	return decision, matched
}

// byRuleFor returns the decision of the rules (see byRule) for a resource
// the file doesn't list, which the default selection excludes with reason
// or includes if reason is "", and whether a rule decides it. A rule
// doesn't include what another Terraform state or tool manages (see
// owned): that stays excluded with reason, unless IncludeOwned is set.
// Only its entry includes it then.
func (f *File) byRuleFor(resourceType, id, name string, tags map[string]string, reason string) (Decision, bool) {
	d, ok := f.byRule(resourceType, id, name, tags)
	if ok && d.Include && !f.IncludeOwned && owned(reason) {
		return Decision{Reason: reason}, true
	}
	return d, ok
}

func (m *Match) matches(resourceType, id, name string, tags map[string]string) bool {
	if !m.Type.match(resourceType) || !m.ID.match(id) || !m.Name.match(name) {
		return false
	}
	for key, patterns := range m.Tags {
		if !patterns.match(tags[key]) {
			return false
		}
	}
	return true
}

// Matches reports whether value matches a pattern, in which * matches any
// text; no patterns match all.
func (p Patterns) Matches(value string) bool {
	return p.match(value)
}

// match reports whether value matches a pattern; no patterns match all.
func (p Patterns) match(value string) bool {
	if len(p) == 0 {
		return true
	}
	for _, pattern := range p {
		if !strings.Contains(pattern, "*") {
			if pattern == value {
				return true
			}
			continue
		}
		if glob(pattern).MatchString(value) {
			return true
		}
	}
	return false
}

// globs holds the compiled glob patterns, by pattern: rules are matched
// against every listed resource, and a file has few patterns.
var globs sync.Map

// glob returns pattern compiled, once per pattern.
func glob(pattern string) *regexp.Regexp {
	if re, ok := globs.Load(pattern); ok {
		return re.(*regexp.Regexp)
	}
	parts := strings.Split(pattern, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	re := regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
	globs.Store(pattern, re)
	return re
}

// Load reads and checks a selection file.
func Load(path string) (*File, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	f := &File{}
	if err := decoder.Decode(f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Version != Version {
		return nil, fmt.Errorf("%s: version %d isn't supported; this infraharvest reads version %d", path, f.Version, Version)
	}
	for i, rule := range f.Rules {
		if (rule.Include == nil) == (rule.Exclude == nil) {
			return nil, fmt.Errorf("%s: rule %d must have one of include and exclude", path, i+1)
		}
	}
	return f, nil
}

// header explains a selection file to the people who edit it.
const header = `# infraharvest selection file: which listed resources to import.
#
# infraharvest discover wrote one entry per resource it found. Set include
# to false to leave a resource out, or true to bring in one the default
# rules excluded (the reason says why they did). Then run:
#
#   infraharvest import <provider> --selection <this file> ...
#
# Resources without an entry, such as new ones, follow the rules (the last
# matching one decides) and then the defaults. Rules match type, id, name
# and tags with * as a wildcard, for example:
#
#   rules:
#     - exclude: { type: aws_cloudwatch_log_group, id: "/aws/lambda/*" }
#     - exclude: { tags: { env: dev } }
#
# A rule doesn't include what another Terraform state or tool manages, such
# as what a CloudFormation stack does: only its entry, or --include-owned,
# includes it.
#
# Running discover again updates this file: entries keep their decisions and
# notes, but those discover made follow what manages each resource now (an
# entry with a note, or one you included or excluded, stays as you left it),
# resources no longer found where it listed are dropped (entries of
# accounts, regions and services it didn't list stay), and new ones are
# added with new: true, decided by the rules and defaults. Review them, then
# remove the mark.
#
`

// Save writes the file with its resources in a stable order.
func (f *File) Save(path string) error {
	sort.SliceStable(f.Resources, func(i, j int) bool {
		a, b := f.Resources[i], f.Resources[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Scope < b.Scope
	})
	var out bytes.Buffer
	out.WriteString(header)
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

// ErrNoSelection explains that an import must say what to import.
var ErrNoSelection = errors.New("say what to import: --selection with a file from infraharvest discover, or --all for everything the default rules select")

// Has reports whether the file lists the resource of resourceType with id
// in any scope.
func (f *File) Has(resourceType, id string) bool {
	_, ok := f.lookup("", resourceType, id)
	return ok
}

// HasIn reports whether the file lists the resource of resourceType with
// id in scope: an entry in that scope, or one listed without a scope.
func (f *File) HasIn(scope, resourceType, id string) bool {
	_, ok := f.lookup(scope, resourceType, id)
	return ok
}

// key identifies an entry: the same type and ID can be listed in several
// scopes, such as one IAM role name in two accounts.
func key(scope, resourceType, id string) string {
	return scope + "\n" + resourceType + " " + id
}

// lookup returns the entry for the resource of resourceType with id in
// scope: the one listed in that scope, else one listed without a scope
// (files written before discover recorded scopes have none). With no scope
// given, an entry in any scope counts, a scope-less one first.
func (f *File) lookup(scope, resourceType, id string) (*Resource, bool) {
	f.index()
	if r, ok := f.byKey[key(scope, resourceType, id)]; ok {
		return r, true
	}
	if scope != "" {
		r, ok := f.byKey[key("", resourceType, id)]
		return r, ok
	}
	r, ok := f.anyScope[resourceType+" "+id]
	return r, ok
}

func (f *File) index() {
	f.indexMu.Lock()
	defer f.indexMu.Unlock()
	if f.byKey != nil {
		return
	}
	f.byKey = make(map[string]*Resource, len(f.Resources))
	f.anyScope = make(map[string]*Resource, len(f.Resources))
	for i := range f.Resources {
		r := &f.Resources[i]
		f.byKey[key(r.Scope, r.Type, r.ID)] = r
		if _, ok := f.anyScope[r.Type+" "+r.ID]; !ok || r.Scope == "" {
			f.anyScope[r.Type+" "+r.ID] = r
		}
	}
}

// NewFile returns the selection file a first discover writes: what it
// listed, each resource once, and what the defaults don't exclude
// included. Nothing in a first file is New.
func NewFile(listed []Resource) *File {
	f := &File{Version: Version, Defaults: Defaults{Include: true}}
	f.Merge(listed, nil)
	for i := range f.Resources {
		f.Resources[i].New = false
	}
	return f
}

// Merge updates f with the resources discover listed now. Entries f has
// keep their decisions and notes, but for the decisions infraharvest made,
// which follow what manages the resource now (see refresh), marked New for
// review where that changes. Listed resources it lacks are added with New
// set: decided by f's rules if one matches (see byRuleFor), else excluded
// with listed's reason if listed excludes them (the provider's defaults),
// else by f's defaults. An entry no longer listed is dropped only if covered reports
// that this run listed everything that would list it, such as its service
// in its scope; any other stays with its decision, as an entry of another
// region, account or service does, or of a listing that failed in part.
// A nil covered covers every entry. It returns how many were added and
// dropped.
func (f *File) Merge(listed []Resource, covered func(Resource) bool) (added, dropped int) {
	f.index()
	seen := map[string]bool{}
	kept := map[string]bool{}
	var merged []Resource
	for _, l := range listed {
		k := key(l.Scope, l.Type, l.ID)
		if seen[k] {
			continue
		}
		seen[k] = true
		if r, ok := f.lookup(l.Scope, l.Type, l.ID); ok {
			kept[key(r.Scope, r.Type, r.ID)] = true
			m := *r
			// Where it is listed, and its tags, are discover's to say.
			m.Scope, m.Tags = l.Scope, l.Tags
			if l.Service != "" {
				m.Service = l.Service
			}
			f.refresh(&m, l)
			merged = append(merged, m)
			continue
		}
		if d, ok := f.byRuleFor(l.Type, l.ID, l.Name, l.Tags, l.Reason); ok {
			l.Include, l.Reason = d.Include, d.Reason
		} else if l.Include {
			d := f.DecideIn(l.Scope, l.Type, l.ID, l.Name, l.Tags)
			l.Include, l.Reason = d.Include, d.Reason
		}
		l.New = true
		merged = append(merged, l)
		added++
	}
	for _, r := range f.Resources {
		if kept[key(r.Scope, r.Type, r.ID)] {
			continue
		}
		if covered != nil && !covered(r) {
			merged = append(merged, r)
			continue
		}
		dropped++
	}
	f.Resources = merged
	f.byKey, f.anyScope = nil, nil
	return added, dropped
}

// refresh brings the decision of entry m up to date with l, the same
// resource as discover lists it now, where the decision is infraharvest's
// rather than people's. People decide an entry with a note, one they
// include although the default selection excluded it (it keeps the
// reason), one they exclude (without a reason), and, by the file's rules
// and defaults, those with a reason of the selection file's own (see
// report.OwnerSelection). Those stay as they are. Otherwise:
//
//   - an entry included without a reason is excluded once Terraform,
//     CloudFormation or another tool manages the resource (see owned);
//   - an entry excluded with infraharvest's reason takes the reason l has
//     now, where Refresh says the run worked that kind out again, or, if l
//     has none, is decided again by the rules and defaults, as a new entry
//     would be.
//
// A change of what manages the resource, or of whether it is included,
// marks the entry New for review.
func (f *File) refresh(m *Resource, l Resource) {
	switch {
	case m.Note != "":
	case m.Include:
		if m.Reason == "" && owned(l.Reason) {
			m.Include, m.Reason, m.New = false, l.Reason, true
		}
	case m.Reason == "" || report.OwnerOf(m.Reason) == report.OwnerSelection || m.Reason == l.Reason:
	case !f.Refresh.covers(m.Reason):
	case l.Reason != "":
		if report.OwnerOf(l.Reason) != report.OwnerOf(m.Reason) {
			m.New = true
		}
		m.Reason = l.Reason
	default:
		d, ok := f.byRuleFor(l.Type, l.ID, l.Name, l.Tags, "")
		if !ok {
			d = Decision{Include: f.Defaults.Include}
			if !d.Include {
				d.Reason = report.ReasonSelectionDefaults
			}
		}
		m.Include, m.Reason, m.New = d.Include, d.Reason, true
	}
}

// isManaged reports whether reason says Terraform already manages the
// resource (see managed.Reason).
func isManaged(reason string) bool {
	return strings.HasPrefix(reason, managed.Reason)
}

// owned reports whether reason, why the default selection excludes a
// resource, says another Terraform state or tool manages it: the state
// --managed-state reads, its tags, or the CloudFormation stack that lists
// it (see managed.Reason and report.OtherToolPrefix). What the cloud
// creates and manages itself, such as a default VPC, isn't owned.
func owned(reason string) bool {
	return isManaged(reason) || strings.HasPrefix(reason, report.OtherToolPrefix)
}

// Summary counts what a selection file includes and excludes, each entry
// as an import of the file decides it.
type Summary struct {
	Included int
	Excluded int
	// Types are the counts by type, sorted by type.
	Types []TypeCount
	// Exclusions are the entries excluded, in the file's order, with the
	// reasons.
	Exclusions []Exclusion
}

// TypeCount counts the entries of one type.
type TypeCount struct {
	Type     string
	Included int
	Excluded int
}

// Exclusion is an entry the file excludes, and why.
type Exclusion struct {
	Type   string
	ID     string
	Reason string
}

// Summary counts what f includes and excludes.
func (f *File) Summary() Summary {
	var s Summary
	byType := map[string]*TypeCount{}
	for _, r := range f.Resources {
		c, ok := byType[r.Type]
		if !ok {
			c = &TypeCount{Type: r.Type}
			byType[r.Type] = c
		}
		d := f.DecideIn(r.Scope, r.Type, r.ID, r.Name, r.Tags)
		if d.Include {
			c.Included++
			s.Included++
			continue
		}
		c.Excluded++
		s.Excluded++
		s.Exclusions = append(s.Exclusions, Exclusion{Type: r.Type, ID: r.ID, Reason: d.Reason})
	}
	for _, c := range byType {
		s.Types = append(s.Types, *c)
	}
	sort.Slice(s.Types, func(i, j int) bool { return s.Types[i].Type < s.Types[j].Type })
	return s
}
