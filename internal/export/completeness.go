// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"slices"

	"github.com/IgnatG/infraharvest/internal/report"
)

// completeness is what the report says of how whole the listing of each
// scope was (see report.Listing), for the inventory to say positively which
// types and scopes it can vouch for. Nothing here is a value of a filter:
// the report keeps only their kinds. Without listings, as in a report an
// earlier release wrote, it says nothing, and a reader takes that as not
// complete.
type completeness struct {
	listings []report.Listing
}

func newCompleteness(listings []report.Listing) completeness {
	return completeness{listings: listings}
}

// filters returns the kinds of filter that narrowed any scope's listing, or
// nil if the report doesn't say.
func (c completeness) filters() *filters {
	if len(c.listings) == 0 {
		return nil
	}
	has := func(kind string) bool {
		return slices.ContainsFunc(c.listings, func(l report.Listing) bool { return slices.Contains(l.Filters, kind) })
	}
	return &filters{
		ID:       has(report.FilterID),
		Tag:      has(report.FilterTag),
		Where:    has(report.FilterWhere),
		Services: has(report.FilterServices),
		Group:    has(report.FilterGroup),
		Partial:  has(report.FilterPartial),
	}
}

// ofType says whether every scope listed all resources of typ, or nil if
// the report doesn't say how the scopes were listed.
func (c completeness) ofType(typ string) *bool {
	if len(c.listings) == 0 {
		return nil
	}
	complete := !slices.ContainsFunc(c.listings, func(l report.Listing) bool { return !slices.Contains(l.Types, typ) })
	return &complete
}

// scopeCounts returns the counts of scope with how whole its listing was.
func (c completeness) scopeCounts(scope string, n counts) scopeCounts {
	out := scopeCounts{Scope: scope, Counts: n}
	if len(c.listings) == 0 {
		return out
	}
	complete := false
	if i := slices.IndexFunc(c.listings, func(l report.Listing) bool { return l.Scope == scope }); i >= 0 {
		complete = c.listings[i].Complete
		out.CompleteTypes = c.listings[i].Types
	}
	out.Complete = &complete
	return out
}
