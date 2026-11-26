// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// scopeListing says how whole the listing of the call c was, for the
// report to give a reader who wants to know whether a resource that isn't
// listed is gone (see report.Listing): which kinds of filter narrowed it,
// whether a service of it failed or it came from a saved inventory, and
// which types it lists in full in the call's scope (see typesListedInFull),
// less those a filter or a failed service touched. It records kinds of
// filter, never what they match.
func scopeListing(provider harvest.Provider, c *callContext, listed listing, failures []error) report.Listing {
	options := c.options
	l := report.Listing{Scope: c.scope, Filters: narrowedBy(c)}
	failed, failedAll := failedServices(failures)
	l.Complete = len(l.Filters) == 0 && len(failed) == 0 && !failedAll && !listed.saved
	// Without a service to tell the types apart, nothing is listed in full:
	// these touch every type.
	if listed.saved || failedAll || slices.ContainsFunc(l.Filters, func(k string) bool {
		return k == report.FilterPartial || k == report.FilterGroup || k == report.FilterTag || k == report.FilterWhere
	}) {
		return l
	}
	filters, err := options.filters()
	if err != nil {
		return l
	}
	inventoryServices := provider.Profile().Layout.InventoryServices
	inventoryFailed := slices.ContainsFunc(inventoryServices, func(s string) bool { return slices.Contains(failed, s) })
	for typ := range typesListedInFull(provider, c) {
		_, name, _ := strings.Cut(typ, "_")
		if slices.ContainsFunc(filters, func(f harvest.Filter) bool { return f.AppliesTo(name) }) {
			continue
		}
		if e, ok := c.cat.View(typ); ok && (slices.Contains(failed, e.Service) || e.Inventory != nil && inventoryFailed) {
			continue
		}
		l.Types = append(l.Types, typ)
	}
	return l
}

// narrowedBy returns the kinds of filter that narrow the listing of the
// call c (see report.FilterKinds): --id, --tag and --where, --services
// that names services instead of "*" and --skip, which may leave some out,
// a provider scope that lists one group, and one that lists in part.
func narrowedBy(c *callContext) []string {
	options := c.options
	var kinds []string
	for _, k := range []struct {
		kind string
		on   bool
	}{
		{report.FilterID, len(options.IDFilters) > 0},
		{report.FilterTag, len(options.TagFilters) > 0},
		{report.FilterWhere, len(options.WhereFilters) > 0},
		{report.FilterServices, !slices.Contains(options.Services, "*") || len(options.Skip) > 0},
		{report.FilterGroup, options.scopeGroup() != ""},
		{report.FilterPartial, options.listsInPart() != ""},
	} {
		if k.on {
			kinds = append(kinds, k.kind)
		}
	}
	return kinds
}

// failedServices returns the services of the failures of a listing, and
// whether a failure names none, so that any service may have failed. A
// failure to read tags leaves out no resource, so it counts for neither.
func failedServices(failures []error) (services []string, unnamed bool) {
	for _, f := range failures {
		msg := f.Error()
		if strings.HasPrefix(msg, tagsFailure) {
			continue
		}
		name, ok := strings.CutPrefix(msg, "service ")
		if !ok {
			unnamed = true
			continue
		}
		name, _, ok = strings.Cut(name, ":")
		if !ok {
			unnamed = true
			continue
		}
		services = append(services, name)
	}
	return services, unnamed
}
