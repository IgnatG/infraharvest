// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package catalogue

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// With returns c with the entries of other, the catalogue of a provider
// whose resources an import of c's provider may also bring in, such as
// azapi's for Azure's resources azurerm has no type for: other's types
// that c has no entry for, and not other's entry for every type, as c's
// own rules for every type cover those resources too. c stays as it was.
func (c *Catalogue) With(other *Catalogue) *Catalogue {
	if other == nil || len(other.entries) == 0 {
		return c
	}
	merged := c.clone()
	for typ, e := range other.entries {
		if _, ok := merged.entries[typ]; ok || typ == AllTypes {
			continue
		}
		copied := *e
		merged.entries[typ] = &copied
		merged.sources[typ] = other.sources[typ]
	}
	return merged
}

func (c *Catalogue) clone() *Catalogue {
	clone := &Catalogue{provider: c.provider, entries: make(map[string]*Entry, len(c.entries)), sources: make(map[string]string, len(c.sources))}
	for typ, e := range c.entries {
		copied := *e
		clone.entries[typ] = &copied
	}
	for typ, s := range c.sources {
		clone.sources[typ] = s
	}
	return clone
}

// copy returns a copy of e that shares nothing with it but its import ID
// templates, which nothing changes once parsed.
func (e *Entry) copy() *Entry {
	copied := *e
	copied.ImportID = slices.Clone(e.ImportID)
	copied.Omit, copied.Defaults, copied.StateOnly = slices.Clone(e.Omit), slices.Clone(e.Defaults), slices.Clone(e.StateOnly)
	copied.KeepEmpty, copied.Children, copied.Remove = slices.Clone(e.KeepEmpty), slices.Clone(e.Children), slices.Clone(e.Remove)
	if e.ProviderVersions != nil {
		copied.ProviderVersions = make(map[string]*Versioned, len(e.ProviderVersions))
		for constraint, x := range e.ProviderVersions {
			if x != nil {
				x = &Versioned{Omit: slices.Clone(x.Omit), Defaults: slices.Clone(x.Defaults), StateOnly: slices.Clone(x.StateOnly)}
			}
			copied.ProviderVersions[constraint] = x
		}
	}
	if e.Skip != nil {
		copied.Skip = make([]SkipRule, len(e.Skip))
		for i, r := range e.Skip {
			r.IDs, r.IDPrefixes, r.Attributes = slices.Clone(r.IDs), slices.Clone(r.IDPrefixes), maps.Clone(r.Attributes)
			r.AttributesPresent = slices.Clone(r.AttributesPresent)
			r.AttributePrefixes, r.AttributeSuffixes = cloneLists(r.AttributePrefixes), cloneLists(r.AttributeSuffixes)
			copied.Skip[i] = r
		}
	}
	copied.DataSource, copied.ARN, copied.CloudControl = clonePointer(e.DataSource), clonePointer(e.ARN), clonePointer(e.CloudControl)
	if e.CloudFormationTypes != nil {
		copied.CloudFormationTypes = make([]CloudFormationType, len(e.CloudFormationTypes))
		for i, t := range e.CloudFormationTypes {
			copied.CloudFormationTypes[i] = CloudFormationType{Type: t.Type, Also: slices.Clone(t.Also)}
		}
	}
	if r := e.ResourceExplorer; r != nil {
		copied.ResourceExplorer = &ResourceExplorer{FromARN: r.FromARN, Types: slices.Clone(r.Types)}
	}
	copied.Inventory, copied.Lifecycle, copied.Placeholder = clonePointer(e.Inventory), clonePointer(e.Lifecycle), e.Placeholder.copy()
	copyGeneration(&copied, e)
	return &copied
}

// copy returns a copy of p that shares nothing with it, nil for nil.
func (p *Placeholder) copy() *Placeholder {
	if p == nil {
		return nil
	}
	return &Placeholder{OneOf: slices.Clone(p.OneOf), Set: maps.Clone(p.Set), Ignore: slices.Clone(p.Ignore), Reason: p.Reason}
}

// clonePointer returns a pointer to a copy of what p points to, nil for
// nil: for the entry's fields of strings and bools alone.
func clonePointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	copied := *p
	return &copied
}

// cloneLists returns a copy of lists that shares no list with it.
func cloneLists(lists map[string][]string) map[string][]string {
	if lists == nil {
		return nil
	}
	copied := make(map[string][]string, len(lists))
	for k, l := range lists {
		copied[k] = slices.Clone(l)
	}
	return copied
}

// merge returns e with the fields overlay sets replaced.
func (e *Entry) merge(overlay *Entry) *Entry {
	merged := *e
	if overlay.Service != "" {
		merged.Service = overlay.Service
	}
	if overlay.ImportID != nil {
		merged.ImportID = overlay.ImportID
		merged.NotImportable = ""
	}
	if overlay.NotImportable != "" {
		merged.NotImportable = overlay.NotImportable
		merged.ImportID = nil
	}
	if overlay.Omit != nil {
		merged.Omit = overlay.Omit
	}
	if overlay.Defaults != nil {
		merged.Defaults = overlay.Defaults
	}
	if overlay.StateOnly != nil {
		merged.StateOnly = overlay.StateOnly
	}
	if overlay.ProviderVersions != nil {
		merged.ProviderVersions = overlay.ProviderVersions
	}
	if overlay.KeepEmpty != nil {
		merged.KeepEmpty = overlay.KeepEmpty
	}
	if overlay.Skip != nil {
		merged.Skip = overlay.Skip
	}
	if overlay.Children != nil {
		merged.Children = overlay.Children
	}
	if overlay.DataSource != nil {
		merged.DataSource = overlay.DataSource
	}
	if overlay.ARN != nil {
		merged.ARN = overlay.ARN
	}
	if overlay.CloudControl != nil {
		merged.CloudControl = overlay.CloudControl
	}
	if overlay.CloudFormationTypes != nil {
		merged.CloudFormationTypes = overlay.CloudFormationTypes
	}
	if overlay.ResourceExplorer != nil {
		merged.ResourceExplorer = overlay.ResourceExplorer
	}
	if overlay.Inventory != nil {
		merged.Inventory = overlay.Inventory
	}
	if overlay.Placeholder != nil {
		merged.Placeholder = overlay.Placeholder
	}
	if overlay.Lifecycle != nil {
		merged.Lifecycle = overlay.Lifecycle
	}
	for _, name := range overlay.Remove {
		if field, ok := removable[name]; ok {
			field.clear(&merged)
		}
	}
	merged.Remove = nil
	return &merged
}

// checkRemove reports what's wrong with an overlay entry's remove: a name
// that isn't a removable field, one named twice, or one the entry sets
// too.
func checkRemove(overlay *Entry) []error {
	var errs []error
	seen := map[string]bool{}
	for _, name := range overlay.Remove {
		field, ok := removable[name]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("remove: %q: want %s", name, strings.Join(slices.Sorted(maps.Keys(removable)), ", ")))
		case seen[name]:
			errs = append(errs, fmt.Errorf("remove: %s twice", name))
		case field.set(overlay):
			errs = append(errs, fmt.Errorf("remove: %s is set too", name))
		}
		seen[name] = true
	}
	return errs
}
