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
	"github.com/IgnatG/infraharvest/managed"
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

	// byKey indexes Resources by scope, type and ID (see key); anyScope by
	// type and ID only, for lookups that don't know the scope. indexMu
	// guards building them: the accounts of one import decide in parallel.
	// Merge is not safe for concurrent use.
	indexMu  sync.Mutex
	byKey    map[string]*Resource
	anyScope map[string]*Resource
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

// Match selects resources by type, ID and name, each a list of patterns in
// which * matches any text. A resource matches if every field given has a
// pattern it matches.
type Match struct {
	Type Patterns `yaml:"type,omitempty"`
	ID   Patterns `yaml:"id,omitempty"`
	Name Patterns `yaml:"name,omitempty"`
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
	Scope   string `yaml:"scope,omitempty"`
	Include bool   `yaml:"include"`
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

// Decide returns the decision for the resource of resourceType with id and
// name, whatever scope it is listed in (see DecideIn).
func (f *File) Decide(resourceType, id, name string) Decision {
	return f.DecideIn("", resourceType, id, name)
}

// DecideIn returns the decision for the resource of resourceType with id
// and name in scope: its entry in Resources if it has one (see HasIn), else
// the last rule that matches it, else Defaults.
func (f *File) DecideIn(scope, resourceType, id, name string) Decision {
	if r, ok := f.lookup(scope, resourceType, id); ok {
		if r.Include {
			return Decision{Include: true}
		}
		reason := r.Reason
		if reason == "" {
			reason = "excluded in the selection file"
		}
		return Decision{Reason: reason}
	}
	if d, ok := f.ByRule(resourceType, id, name); ok {
		return d
	}
	if f.Defaults.Include {
		return Decision{Include: true}
	}
	return Decision{Reason: "excluded by the selection file's defaults"}
}

// ByRule returns the decision of the last rule that matches the resource
// of resourceType with id and name, and whether one does.
func (f *File) ByRule(resourceType, id, name string) (Decision, bool) {
	var decision Decision
	matched := false
	for _, rule := range f.Rules {
		if rule.Include != nil && rule.Include.matches(resourceType, id, name) {
			decision, matched = Decision{Include: true}, true
		}
		if rule.Exclude != nil && rule.Exclude.matches(resourceType, id, name) {
			decision, matched = Decision{Reason: "excluded by a rule in the selection file"}, true
		}
	}
	return decision, matched
}

func (m *Match) matches(resourceType, id, name string) bool {
	return m.Type.match(resourceType) && m.ID.match(id) && m.Name.match(name)
}

// match reports whether value matches a pattern; no patterns match all.
func (p Patterns) match(value string) bool {
	if len(p) == 0 {
		return true
	}
	for _, pattern := range p {
		if glob(pattern).MatchString(value) {
			return true
		}
	}
	return false
}

func glob(pattern string) *regexp.Regexp {
	parts := strings.Split(pattern, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
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
# matching one decides) and then the defaults. Rules match type, id and name
# with * as a wildcard, for example:
#
#   rules:
#     - exclude: { type: aws_cloudwatch_log_group, id: "/aws/lambda/*" }
#
# Running discover again updates this file: entries keep their decisions and
# notes, resources no longer found are dropped, and new ones are added with
# new: true, decided by the rules and defaults. Review them, then remove the
# mark.
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

// Merge updates f with the resources discover listed now. Entries f has
// keep their decisions and notes, except that one included without a note
// is excluded once Terraform manages the resource (listed's reason says
// so), marked New for review. Listed resources it lacks are added with New
// set: decided by f's rules if one matches, else excluded with listed's
// reason if listed excludes them (the provider's defaults), else by f's
// defaults. Entries no longer listed are dropped. It returns how many were
// added and dropped.
func (f *File) Merge(listed []Resource) (added, dropped int) {
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
			// Where it is listed is discover's to say.
			m.Scope = l.Scope
			if m.Include && m.Note == "" && isManaged(l.Reason) {
				m.Include, m.Reason, m.New = false, l.Reason, true
			}
			merged = append(merged, m)
			continue
		}
		if d, ok := f.ByRule(l.Type, l.ID, l.Name); ok {
			l.Include, l.Reason = d.Include, d.Reason
		} else if l.Include {
			d := f.DecideIn(l.Scope, l.Type, l.ID, l.Name)
			l.Include, l.Reason = d.Include, d.Reason
		}
		l.New = true
		merged = append(merged, l)
		added++
	}
	for _, r := range f.Resources {
		if !kept[key(r.Scope, r.Type, r.ID)] {
			dropped++
		}
	}
	f.Resources = merged
	f.byKey, f.anyScope = nil, nil
	return added, dropped
}

// isManaged reports whether reason says Terraform already manages the
// resource (see managed.Reason).
func isManaged(reason string) bool {
	return strings.HasPrefix(reason, managed.Reason)
}
