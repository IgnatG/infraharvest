// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package catalogue

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/go-version"
)

// Omit returns the arguments to leave out of configuration generated
// with version v of the provider, per type and under AllTypes for every
// type: the entries whose conditions don't depend on what a lister
// recorded (see ResourceOmit), and what the entries' provider_versions
// add for v. A nil v adds nothing: what holds whatever the version.
func (c *Catalogue) Omit(v *version.Version) map[string][]string {
	return c.lists(v, func(e *Entry) []string { return withoutListerConditions(e.Omit) }, func(x *Versioned) []string { return withoutListerConditions(x.Omit) })
}

// withoutListerConditions returns the omit entries without a condition on
// what a lister recorded (see ResourceOmit).
func withoutListerConditions(entries []string) []string {
	return slices.DeleteFunc(slices.Clone(entries), func(entry string) bool {
		_, conditions := listerConditions(entry)
		return len(conditions) > 0
	})
}

// ResourceOmit returns the arguments to leave out of the generated
// configuration of one resource of typ besides those Omit returns: the
// entries with a condition on what its lister recorded, @attribute=value
// (or @attribute, set and not empty, or !@attribute, not), where those
// conditions hold for attributes, without them.
func (c *Catalogue) ResourceOmit(typ string, attributes map[string]string) []string {
	e, ok := c.entries[typ]
	if !ok {
		return nil
	}
	var list []string
	for _, entry := range e.Omit {
		rest, conditions := listerConditions(entry)
		if len(conditions) > 0 && !slices.ContainsFunc(conditions, func(cond string) bool { return !listerConditionHolds(cond, attributes) }) {
			list = append(list, rest)
		}
	}
	return list
}

// listerConditions splits an omit entry into the entry without its
// conditions on what the lister recorded, and those conditions.
func listerConditions(entry string) (string, []string) {
	target, conditions, ok := strings.Cut(entry, " when ")
	if !ok {
		return entry, nil
	}
	var kept, lister []string
	for _, c := range strings.Split(conditions, " and ") {
		c = strings.TrimSpace(c)
		if strings.HasPrefix(strings.TrimPrefix(c, "!"), "@") {
			lister = append(lister, c)
		} else {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		return target, lister
	}
	return target + " when " + strings.Join(kept, " and "), lister
}

// listerConditionHolds reports whether a condition on what a lister
// recorded, @name=value, @name or !@name, holds for attributes.
func listerConditionHolds(condition string, attributes map[string]string) bool {
	negate := strings.HasPrefix(condition, "!")
	name, value, valued := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(condition, "!"), "@"), "=")
	got := attributes[name]
	holds := got != ""
	if valued {
		holds = got == value
	}
	return holds != negate
}

// Defaults returns the values optional arguments default to with version
// v of the provider, as path=value, per type: what holds whatever the
// version, and what the entries' provider_versions add for v, such as a
// default a major release changed. A nil v adds nothing.
func (c *Catalogue) Defaults(v *version.Version) map[string][]string {
	return c.lists(v, func(e *Entry) []string { return e.Defaults }, func(x *Versioned) []string { return x.Defaults })
}

// StateOnly returns the arguments version v of the provider keeps only in
// state, per type and under AllTypes for every type: what holds whatever
// the version, and what the entries' provider_versions add for v. A nil v
// adds nothing.
func (c *Catalogue) StateOnly(v *version.Version) map[string][]string {
	return c.lists(v, func(e *Entry) []string { return e.StateOnly }, func(x *Versioned) []string { return x.StateOnly })
}

// lists returns each entry's field, with what its provider_versions add
// for v in constraint order, by type.
func (c *Catalogue) lists(v *version.Version, field func(*Entry) []string, versioned func(*Versioned) []string) map[string][]string {
	lists := map[string][]string{}
	for typ, e := range c.entries {
		l := append([]string(nil), field(e)...)
		if v != nil {
			for _, constraint := range slices.Sorted(maps.Keys(e.ProviderVersions)) {
				if accepts(constraint, v) && e.ProviderVersions[constraint] != nil {
					l = append(l, versioned(e.ProviderVersions[constraint])...)
				}
			}
		}
		if len(l) > 0 {
			lists[typ] = l
		}
	}
	return lists
}

// accepts reports whether the version constraint accepts v; check
// rejects constraints that don't parse.
func accepts(constraint string, v *version.Version) bool {
	c, err := version.NewConstraint(constraint)
	return err == nil && c.Check(v)
}

// KeepEmpty returns the arguments that stay where they are set empty,
// per type.
func (c *Catalogue) KeepEmpty() map[string][]string {
	return c.lists(nil, func(e *Entry) []string { return e.KeepEmpty }, func(*Versioned) []string { return nil })
}

// ARNs returns where the ARNs of each type that says so name its
// resources.
func (c *Catalogue) ARNs() map[string]ARN {
	arns := map[string]ARN{}
	for typ, e := range c.entries {
		if e.ARN != nil {
			arns[typ] = *e.ARN
		}
	}
	return arns
}

// DataSources returns the data source of each type that has one.
func (c *Catalogue) DataSources() map[string]DataSource {
	sources := map[string]DataSource{}
	for typ, e := range c.entries {
		if e.DataSource != nil {
			sources[typ] = *e.DataSource
		}
	}
	return sources
}

// ImportID returns the ID Terraform imports a resource of typ with, from
// the ID its lister recorded and its attributes. It returns false, with
// the reason, when Terraform can't import it: the type isn't importable,
// or no template's fields all have values.
func (c *Catalogue) ImportID(typ, id string, attributes map[string]string) (string, bool, string) {
	e, ok := c.entries[typ]
	if !ok {
		return id, true, ""
	}
	if e.NotImportable != "" {
		return "", false, e.NotImportable
	}
	if len(e.ImportID) == 0 {
		return id, true, ""
	}
	for _, t := range e.ImportID {
		if rendered, ok := t.Render(id, attributes); ok {
			return rendered, true, ""
		}
	}
	return "", false, fmt.Sprintf("missing a field of every import ID template: %s", templateList(e.ImportID))
}

// FallbackImportID returns the import ID of a resource of typ to import it
// by should Terraform reject the one ImportID gives: that of the next of
// the entry's templates that renders, and renders otherwise, such as an
// azapi_resource's ID without the ?api-version= of a version azapi may not
// know. It reports false where there is none.
func (c *Catalogue) FallbackImportID(typ, id string, attributes map[string]string) (string, bool) {
	e, ok := c.entries[typ]
	if !ok || e.NotImportable != "" {
		return "", false
	}
	first := ""
	for _, t := range e.ImportID {
		rendered, ok := t.Render(id, attributes)
		switch {
		case !ok:
		case first == "":
			first = rendered
		case rendered != first:
			return rendered, true
		}
	}
	return "", false
}

// SkippedWhole returns the types whose every resource the default
// selection leaves out, by a skip rule with all, sorted.
func (c *Catalogue) SkippedWhole() []string {
	var types []string
	for typ, e := range c.entries {
		if typ != AllTypes && slices.ContainsFunc(e.Skip, func(r SkipRule) bool { return r.All }) {
			types = append(types, typ)
		}
	}
	sort.Strings(types)
	return types
}

// Skipped returns why the default selection leaves out a resource of typ,
// or "" if it doesn't: the type's rules first, then those for every type.
func (c *Catalogue) Skipped(typ, id string, attributes map[string]string) string {
	for _, t := range []string{typ, AllTypes} {
		e, ok := c.entries[t]
		if !ok {
			continue
		}
		for _, rule := range e.Skip {
			if rule.matches(id, attributes) {
				return rule.Reason
			}
		}
	}
	return ""
}

// FromInventory returns the Terraform types of a resource the cloud's
// inventory API lists as assetType, named name: the types whose inventory
// entry has that type and a name pattern that matches, if it has one,
// sorted. More than one means the catalogue can't tell them apart.
func (c *Catalogue) FromInventory(assetType, name string) []string {
	c.inventoryOnce.Do(c.indexInventory)
	var types []string
	lowerName := ""
	for _, e := range c.byInventory[strings.ToLower(assetType)] {
		if e.name != "" {
			if lowerName == "" {
				lowerName = strings.ToLower(name)
			}
			if ok, _ := path.Match(e.name, lowerName); !ok {
				continue
			}
		}
		types = append(types, e.typ)
	}
	return types
}

// InventoryTypes returns the Terraform types whose inventory entry has
// assetType, whatever its name pattern: those FromInventory may return
// for a resource of that type, sorted.
func (c *Catalogue) InventoryTypes(assetType string) []string {
	c.inventoryOnce.Do(c.indexInventory)
	var types []string
	for _, e := range c.byInventory[strings.ToLower(assetType)] {
		types = append(types, e.typ)
	}
	return types
}

// indexInventory builds byInventory (see FromInventory).
func (c *Catalogue) indexInventory() {
	c.byInventory = map[string][]inventoryEntry{}
	for typ, e := range c.entries {
		if e.Inventory != nil {
			key := strings.ToLower(e.Inventory.Type)
			c.byInventory[key] = append(c.byInventory[key], inventoryEntry{typ: typ, name: strings.ToLower(e.Inventory.Name)})
		}
	}
	for _, list := range c.byInventory {
		sort.Slice(list, func(i, j int) bool { return list[i].typ < list[j].typ })
	}
}

func (r SkipRule) matches(id string, attributes map[string]string) bool {
	equal := func(a, b string) bool { return a == b }
	hasPrefix := strings.HasPrefix
	hasSuffix := strings.HasSuffix
	if r.IgnoreCase {
		equal = strings.EqualFold
		hasPrefix = func(s, prefix string) bool {
			return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
		}
		hasSuffix = func(s, suffix string) bool {
			return len(s) >= len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix)
		}
	}
	if len(r.IDs) > 0 && !slices.ContainsFunc(r.IDs, func(want string) bool { return equal(id, want) }) {
		return false
	}
	if len(r.IDPrefixes) > 0 && !slices.ContainsFunc(r.IDPrefixes, func(p string) bool { return hasPrefix(id, p) }) {
		return false
	}
	for k, v := range r.Attributes {
		if !equal(attributes[k], v) {
			return false
		}
	}
	for _, k := range r.AttributesPresent {
		if _, ok := attributes[k]; !ok {
			return false
		}
	}
	for k, prefixes := range r.AttributePrefixes {
		value, ok := attributes[k]
		if !ok || !slices.ContainsFunc(prefixes, func(p string) bool { return hasPrefix(value, p) }) {
			return false
		}
	}
	for k, suffixes := range r.AttributeSuffixes {
		value, ok := attributes[k]
		if !ok || !slices.ContainsFunc(suffixes, func(s string) bool { return hasSuffix(value, s) }) {
			return false
		}
	}
	return true
}

// Placeholders returns the placeholder of each type that has one.
func (c *Catalogue) Placeholders() map[string]Placeholder {
	placeholders := map[string]Placeholder{}
	for typ, e := range c.entries {
		if e.Placeholder != nil {
			placeholders[typ] = *e.Placeholder.copy()
		}
	}
	return placeholders
}

// Lifecycles returns the lifecycle settings of each type that has them.
func (c *Catalogue) Lifecycles() map[string]Lifecycle {
	lifecycles := map[string]Lifecycle{}
	for typ, e := range c.entries {
		if e.Lifecycle != nil {
			lifecycles[typ] = *e.Lifecycle
		}
	}
	return lifecycles
}
