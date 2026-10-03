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

	"go.yaml.in/yaml/v3"
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

	byKey map[string]*Resource
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
	Type    string `yaml:"type"`
	ID      string `yaml:"id"`
	Name    string `yaml:"name,omitempty"`
	Include bool   `yaml:"include"`
	// Reason says why the default rules exclude the resource.
	Reason string `yaml:"reason,omitempty"`
	// Note is for people; infraharvest keeps it.
	Note string `yaml:"note,omitempty"`
}

// Decision is whether to import a resource, and why not.
type Decision struct {
	Include bool
	Reason  string
}

// Decide returns the decision for the resource of resourceType with id and
// name: its entry in Resources if it has one, else the last rule that
// matches it, else Defaults.
func (f *File) Decide(resourceType, id, name string) Decision {
	f.index()
	if r, ok := f.byKey[resourceType+" "+id]; ok {
		if r.Include {
			return Decision{Include: true}
		}
		reason := r.Reason
		if reason == "" {
			reason = "excluded in the selection file"
		}
		return Decision{Reason: reason}
	}
	decision := Decision{Include: f.Defaults.Include, Reason: "excluded by the selection file's defaults"}
	if decision.Include {
		decision.Reason = ""
	}
	for _, rule := range f.Rules {
		if rule.Include != nil && rule.Include.matches(resourceType, id, name) {
			decision = Decision{Include: true}
		}
		if rule.Exclude != nil && rule.Exclude.matches(resourceType, id, name) {
			decision = Decision{Reason: "excluded by a rule in the selection file"}
		}
	}
	return decision
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
#   infraharvest import <provider> --engine=terraform --selection <this file> ...
#
# Resources without an entry, such as new ones, follow the rules (the last
# matching one decides) and then the defaults. Rules match type, id and name
# with * as a wildcard, for example:
#
#   rules:
#     - exclude: { type: aws_cloudwatch_log_group, id: "/aws/lambda/*" }
#
`

// Save writes the file with its resources in a stable order.
func (f *File) Save(path string) error {
	sort.SliceStable(f.Resources, func(i, j int) bool {
		a, b := f.Resources[i], f.Resources[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.ID < b.ID
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
	return os.WriteFile(path, out.Bytes(), 0o644)
}

// ErrNoSelection explains that an import must say what to import.
var ErrNoSelection = errors.New("say what to import: --selection with a file from infraharvest discover, or --all for everything the default rules select")

// Has reports whether the file lists the resource of resourceType with id.
func (f *File) Has(resourceType, id string) bool {
	f.index()
	_, ok := f.byKey[resourceType+" "+id]
	return ok
}

func (f *File) index() {
	if f.byKey != nil {
		return
	}
	f.byKey = make(map[string]*Resource, len(f.Resources))
	for i := range f.Resources {
		f.byKey[f.Resources[i].Type+" "+f.Resources[i].ID] = &f.Resources[i]
	}
}
