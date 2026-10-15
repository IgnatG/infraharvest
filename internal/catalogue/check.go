// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package catalogue

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
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
	if !identifier.MatchString(c.Provider) {
		errs = append(errs, fmt.Errorf("provider %q: want a provider's local name, such as aws", c.Provider))
	}
	for typ, e := range c.entries {
		for _, err := range c.checkEntry(typ, e) {
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
			e.ARN != nil || e.CloudControl != nil || e.Inventory != nil || e.Service != "" {
			fail("the entry for every type only takes omit, state_only and skip")
		}
	} else if !identifier.MatchString(typ) || !strings.HasPrefix(typ, c.Provider+"_") {
		fail("not a %s resource type", c.Provider)
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
	for _, list := range []struct {
		name  string
		names []string
	}{{"omit", e.Omit}, {"state_only", e.StateOnly}, {"placeholder.one_of", placeholderList(e, func(p *Placeholder) []string { return p.OneOf })}, {"placeholder.ignore", placeholderList(e, func(p *Placeholder) []string { return p.Ignore })}} {
		seen := map[string]bool{}
		for _, name := range list.names {
			argument := name
			if list.name == "omit" {
				// name=value leaves the argument out only when set to value.
				argument, _, _ = strings.Cut(name, "=")
			}
			if !identifier.MatchString(argument) {
				fail("%s: %q isn't an argument name", list.name, name)
			}
			if seen[name] {
				fail("%s: %s twice", list.name, name)
			}
			seen[name] = true
		}
	}
	for i, rule := range e.Skip {
		if strings.TrimSpace(rule.Reason) == "" {
			fail("skip[%d]: missing a reason", i)
		}
		conditions := len(rule.IDs) > 0 || len(rule.IDPrefixes) > 0 || len(rule.Attributes) > 0 || len(rule.AttributePrefixes) > 0
		switch {
		case rule.All && conditions:
			fail("skip[%d]: all matches every resource, so it takes no ids, id_prefixes, attributes or attribute_prefixes", i)
		case !rule.All && !conditions:
			fail("skip[%d]: set ids, id_prefixes, attributes or attribute_prefixes, or all: true for every resource", i)
		}
		for _, p := range rule.IDPrefixes {
			if p == "" {
				fail("skip[%d]: empty ID prefix", i)
			}
		}
		for k, prefixes := range rule.AttributePrefixes {
			if len(prefixes) == 0 || slices.Contains(prefixes, "") {
				fail("skip[%d]: attribute_prefixes: %s: want prefixes, none empty", i, k)
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
	for _, types := range byType {
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

// placeholderList returns a list of e's placeholder, if it has one.
func placeholderList(e *Entry, list func(*Placeholder) []string) []string {
	if e.Placeholder == nil {
		return nil
	}
	return list(e.Placeholder)
}
