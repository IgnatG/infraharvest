// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package catalogue

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
)

var (
	identifier       = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	cloudFormation   = regexp.MustCompile(`^[A-Za-z0-9]+::[A-Za-z0-9]+::[A-Za-z0-9]+$`)
	cloudControlProp = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
	inventoryType    = regexp.MustCompile(`^[A-Za-z0-9.-]+(/[A-Za-z0-9]+)+$`)
)

// check reports what's wrong with the catalogue's entries.
func (c *Catalogue) check() error {
	var errs []error
	if !identifier.MatchString(c.provider) {
		errs = append(errs, fmt.Errorf("provider %q: want a provider's local name, such as aws", c.provider))
	}
	// Sorted, so that the errors come in the same order every run.
	for _, typ := range slices.Sorted(maps.Keys(c.entries)) {
		for _, err := range c.checkEntry(typ, c.entries[typ]) {
			errs = append(errs, fmt.Errorf("%s: %s: %w", c.sources[typ], typ, err))
		}
	}
	errs = append(errs, c.checkInventory()...)
	return errors.Join(errs...)
}

func (c *Catalogue) checkEntry(typ string, e *Entry) []error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if typ == AllTypes {
		if len(e.ImportID) > 0 || e.NotImportable != "" || len(e.Children) > 0 || e.DataSource != nil ||
			e.ARN != nil || e.CloudControl != nil || e.Inventory != nil || e.Service != "" || len(e.Defaults) > 0 || e.Lifecycle != nil {
			fail("the entry for every type only takes omit, state_only, provider_versions and skip")
		}
	} else if !identifier.MatchString(typ) || !strings.HasPrefix(typ, c.provider+"_") {
		fail("not a %s resource type", c.provider)
	}
	if e.Service != "" && !identifier.MatchString(e.Service) {
		fail("service %q: want an infraharvest service name", e.Service)
	}
	if e.NotImportable != "" && len(e.ImportID) > 0 {
		fail("not_importable and import_id are exclusive")
	}
	for _, t := range e.ImportID {
		if len(t.Fields()) == 0 {
			fail("import_id %q has no field: every resource would import with the same ID", t)
		}
	}
	type argumentList struct {
		name  string
		names []string
	}
	lists := []argumentList{{"omit", e.Omit}, {"state_only", e.StateOnly}, {"keep_empty", e.KeepEmpty}, {"placeholder.one_of", placeholderList(e, func(p *Placeholder) []string { return p.OneOf })}, {"placeholder.ignore", placeholderList(e, func(p *Placeholder) []string { return p.Ignore })}}
	defaults := []argumentList{{"defaults", e.Defaults}}
	for _, constraint := range slices.Sorted(maps.Keys(e.ProviderVersions)) {
		if _, err := version.NewConstraint(constraint); err != nil {
			fail("provider_versions: %q isn't a version constraint, such as \">= 5.0\": %v", constraint, err)
		}
		x := e.ProviderVersions[constraint]
		if x == nil || len(x.Omit)+len(x.Defaults)+len(x.StateOnly) == 0 {
			fail("provider_versions: %s sets neither omit, defaults nor state_only", constraint)
			continue
		}
		if typ == AllTypes && len(x.Defaults) > 0 {
			fail("provider_versions: %s: the entry for every type takes no defaults", constraint)
		}
		// What every version has already needs no constraint.
		for _, l := range []argumentList{{"omit", x.Omit}, {"defaults", x.Defaults}, {"state_only", x.StateOnly}} {
			base := map[string][]string{"omit": e.Omit, "defaults": e.Defaults, "state_only": e.StateOnly}[l.name]
			for _, name := range l.names {
				if slices.Contains(base, name) {
					fail("provider_versions: %s: %s: %s is there for every version", constraint, l.name, name)
				}
			}
			versioned := argumentList{"provider_versions: " + constraint + ": " + l.name, l.names}
			if l.name == "defaults" {
				defaults = append(defaults, versioned)
			} else {
				lists = append(lists, versioned)
			}
		}
	}
	for _, list := range lists {
		seen := map[string]bool{}
		for _, name := range list.names {
			if strings.HasSuffix(list.name, "omit") {
				if err := checkOmission(name); err != nil {
					fail("%s: %q: %v", list.name, name, err)
				}
			} else if !identifier.MatchString(name) {
				fail("%s: %q isn't an argument name", list.name, name)
			}
			if seen[name] {
				fail("%s: %s twice", list.name, name)
			}
			seen[name] = true
		}
	}
	if l := e.Lifecycle; l != nil && (!l.CreateBeforeDestroy || strings.TrimSpace(l.Reason) == "") {
		fail("lifecycle: set create_before_destroy: true, with a reason")
	}
	if p := e.Placeholder; p != nil {
		errs = append(errs, checkPlaceholder(p)...)
	}
	errs = append(errs, checkGeneration(typ, e)...)
	if len(e.Remove) > 0 {
		fail("remove: only an overlay of a built-in entry takes remove")
	}
	for _, list := range defaults {
		seen := map[string]bool{}
		for _, entry := range list.names {
			path, _, ok := strings.Cut(entry, "=")
			if !ok {
				fail("%s: %q: want path=value", list.name, entry)
			}
			for _, name := range strings.Split(path, ".") {
				if !identifier.MatchString(name) {
					fail("%s: %q: %q isn't an argument or block name", list.name, entry, name)
				}
			}
			if seen[path] {
				fail("%s: %s twice", list.name, path)
			}
			seen[path] = true
		}
	}
	for i, rule := range e.Skip {
		if strings.TrimSpace(rule.Reason) == "" {
			fail("skip[%d]: missing a reason", i)
		}
		conditions := len(rule.IDs) > 0 || len(rule.IDPrefixes) > 0 || len(rule.Attributes) > 0 || len(rule.AttributePrefixes) > 0 || len(rule.AttributeSuffixes) > 0
		switch {
		case rule.All && conditions:
			fail("skip[%d]: all matches every resource, so it takes no ids, id_prefixes, attributes, attribute_prefixes or attribute_suffixes", i)
		case !rule.All && !conditions:
			fail("skip[%d]: set ids, id_prefixes, attributes, attribute_prefixes or attribute_suffixes, or all: true for every resource", i)
		}
		for _, p := range rule.IDPrefixes {
			if p == "" {
				fail("skip[%d]: empty ID prefix", i)
			}
		}
		for _, k := range slices.Sorted(maps.Keys(rule.AttributePrefixes)) {
			prefixes := rule.AttributePrefixes[k]
			if len(prefixes) == 0 || slices.Contains(prefixes, "") {
				fail("skip[%d]: attribute_prefixes: %s: want prefixes, none empty", i, k)
			}
		}
		for _, k := range slices.Sorted(maps.Keys(rule.AttributeSuffixes)) {
			suffixes := rule.AttributeSuffixes[k]
			if len(suffixes) == 0 || slices.Contains(suffixes, "") {
				fail("skip[%d]: attribute_suffixes: %s: want suffixes, none empty", i, k)
			}
		}
	}
	for _, child := range e.Children {
		if child == typ {
			fail("children: is its own child")
		} else if _, ok := c.entries[child]; !ok {
			fail("children: %s has no entry", child)
		}
	}
	if d := e.DataSource; d != nil && (!identifier.MatchString(d.Type) || !identifier.MatchString(d.Argument)) {
		fail("data_source: want a type and an argument")
	} else if d != nil && !strings.HasPrefix(d.Type, c.provider+"_") {
		fail("data_source: %s isn't a %s data source", d.Type, c.provider)
	}
	if a := e.ARN; a != nil && (a.Service == "" || strings.ContainsAny(a.Service+a.ResourceType, ":/ ")) {
		fail("arn: want an ARN's service and resource type")
	}
	if cc := e.CloudControl; cc != nil {
		if !cloudFormation.MatchString(cc.Type) || !cloudControlProp.MatchString(cc.Identifier) {
			fail("cloud_control: want a CloudFormation type and its identifier's property")
		}
		if e.Service == "" {
			fail("cloud_control: missing the service that lists it")
		}
		if len(e.ImportID) > 0 {
			fail("cloud_control: the identifier is the import ID, so import_id doesn't apply")
		}
	}
	if inv := e.Inventory; inv != nil {
		if !inventoryType.MatchString(inv.Type) {
			fail("inventory: type %q: want an inventory API's type, such as compute.googleapis.com/Network", inv.Type)
		}
		if _, err := path.Match(inv.Name, ""); err != nil {
			fail("inventory: name %q: %v", inv.Name, err)
		}
	}
	return errs
}

// checkInventory reports inventory types that map to several Terraform
// types without name patterns that tell them apart.
func (c *Catalogue) checkInventory() []error {
	byType := map[string][]string{}
	for typ, e := range c.entries {
		if e.Inventory != nil {
			key := strings.ToLower(e.Inventory.Type)
			byType[key] = append(byType[key], typ)
		}
	}
	var errs []error
	for _, key := range slices.Sorted(maps.Keys(byType)) {
		types := byType[key]
		sort.Strings(types)
		for i, a := range types {
			for _, b := range types[i+1:] {
				ia, ib := c.entries[a].Inventory, c.entries[b].Inventory
				if ia.Name == "" || ib.Name == "" || strings.EqualFold(ia.Name, ib.Name) {
					errs = append(errs, fmt.Errorf("%s: %s: inventory: %s is %s too; give each a name pattern that tells them apart", c.sources[a], a, ia.Type, b))
				}
			}
		}
	}
	return errs
}

// placeholderReference is an {argument} in a placeholder value, which
// the engine replaces with the value of one of the resource's arguments.
var placeholderReference = regexp.MustCompile(`\{[a-z0-9_]+\}`)

// checkPlaceholder reports what's wrong with a placeholder, as the
// catalogue's schema does for the built-in files: an overlay is checked
// by this alone. Without set, the placeholder would ignore changes and set
// nothing, so the plan would fail on the missing argument.
func checkPlaceholder(p *Placeholder) []error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if len(p.OneOf) == 0 || len(p.Set) == 0 || len(p.Ignore) == 0 || strings.TrimSpace(p.Reason) == "" {
		fail("placeholder: set one_of, set, ignore and a reason")
	}
	for _, name := range slices.Sorted(maps.Keys(p.Set)) {
		value := p.Set[name]
		switch {
		case !identifier.MatchString(name):
			fail("placeholder.set: %q isn't an argument name", name)
		case value == "":
			fail("placeholder.set: %s: empty value", name)
		case strings.ContainsAny(placeholderReference.ReplaceAllString(value, ""), "{}"):
			fail("placeholder.set: %s: %q: want {argument} for an argument's value, with an argument name between the braces", name, value)
		}
	}
	return errs
}

// placeholderList returns a list of e's placeholder, if it has one.
func placeholderList(e *Entry, list func(*Placeholder) []string) []string {
	if e.Placeholder == nil {
		return nil
	}
	return list(e.Placeholder)
}

// argumentPath is an argument, or one in nested blocks: a.b.c.
var argumentPath = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// omittedPath is what an omit entry leaves out: an argument, or one in
// nested blocks, where * stands for blocks of any type (a.b, *.b).
var omittedPath = regexp.MustCompile(`^(([a-z][a-z0-9_]*|\*)\.)*[a-z][a-z0-9_]*$`)

// checkOmission reports what's wrong with an omit entry: path or
// path=value, optionally followed by " when " and conditions joined by
// " and ", each path, path=value or !path, where a path starting with @
// names what the lister recorded.
func checkOmission(entry string) error {
	target, conditions, hasConditions := strings.Cut(entry, " when ")
	if name, _, _ := strings.Cut(target, "="); !omittedPath.MatchString(name) {
		return fmt.Errorf("%q isn't an argument name or a path to one", name)
	}
	if !hasConditions {
		return nil
	}
	for _, c := range strings.Split(conditions, " and ") {
		name, _, _ := strings.Cut(strings.TrimPrefix(c, "!"), "=")
		if listed, ok := strings.CutPrefix(name, "@"); ok {
			if !identifier.MatchString(listed) {
				return fmt.Errorf("condition %q: %q isn't an attribute name", c, listed)
			}
			continue
		}
		if !argumentPath.MatchString(name) {
			return fmt.Errorf("condition %q: %q isn't an argument name or a path to one", c, name)
		}
	}
	return nil
}
