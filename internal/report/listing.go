// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"slices"
	"sort"
)

// The kinds of filter that leave resources out of a listing (see
// Listing.Filters). Only the kind is recorded, never what the filter
// matches: tag values and IDs stay out of the report.
const (
	// FilterID is --id.
	FilterID = "id"
	// FilterTag is --tag.
	FilterTag = "tag"
	// FilterWhere is --where.
	FilterWhere = "where"
	// FilterServices is --services naming services instead of "*", or
	// --skip: services the provider lists by default may be left out.
	FilterServices = "services"
	// FilterGroup is a provider scope that lists one group of its account,
	// such as Azure's --resource-group.
	FilterGroup = "group"
	// FilterPartial is a provider scope that lists in part, such as AWS's
	// --resource-explorer (see harvest.PartialListingScope).
	FilterPartial = "partial"
)

// FilterKinds are the kinds of filter, in the order documents list them.
var FilterKinds = []string{FilterID, FilterTag, FilterWhere, FilterServices, FilterGroup, FilterPartial}

// Listing says how whole the listing of one scope was, so that a reader
// can tell a resource that isn't listed from one that was never looked
// for. A scope without a Listing, as in a report an earlier release wrote,
// is not known to have been listed in full.
type Listing struct {
	Scope string `json:"scope"`
	// Complete is whether nothing narrowed the scope's listing (no filter
	// (Filters is empty), no part of it failed, and the resources weren't
	// read from an inventory discover saved, which may be older) and Types
	// holds every type of the call: the children of other resources, the
	// types the provider lists in part, those that can't be imported and
	// those the import doesn't list are never in Types, so a scope that has
	// any of them is not complete, and a reader who asks about one type
	// reads Types.
	Complete bool `json:"complete"`
	// Filters are the kinds of filter the listing had (see FilterKinds).
	Filters []string `json:"filters,omitempty"`
	// Types are the types the listing found every resource of in the
	// scope, whether it found any or not: those whose service listed, with
	// no filter or failure touching them. A type of a dependent service,
	// which asks about what the other services listed, is here only if
	// every service it depends on listed and none failed. A type not named here, such as
	// one only a child of another resource has, may have resources the
	// listing didn't find.
	Types []string `json:"complete_types,omitempty"`
}

// AddListing records how the listing of one call was, in its scope. A scope
// several calls list is complete only as every call lists it: their filters
// add up, and it has the types they all list in full.
func (r *Report) AddListing(l Listing) {
	if l.Scope == "" {
		return
	}
	for i := range r.Listings {
		have := &r.Listings[i]
		if have.Scope != l.Scope {
			continue
		}
		have.Complete = have.Complete && l.Complete
		have.Filters = slices.Compact(slices.Sorted(slices.Values(slices.Concat(have.Filters, l.Filters))))
		have.Types = slices.DeleteFunc(have.Types, func(t string) bool { return !slices.Contains(l.Types, t) })
		return
	}
	l.Filters = slices.Compact(slices.Sorted(slices.Values(l.Filters)))
	sort.Strings(l.Types)
	r.Listings = append(r.Listings, l)
}
