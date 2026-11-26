// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"slices"
	"testing"
)

// A report holds how each scope was listed, and a scope that several calls
// list is complete only as all of them list it: their filters add up, and
// it has the types they all list in full. A call without a scope says
// nothing.
func TestAddListing(t *testing.T) {
	t.Parallel()
	var r Report
	r.AddListing(Listing{Scope: "aws/1/r", Complete: true, Types: []string{"b", "a"}})
	r.AddListing(Listing{Scope: "aws/1/r", Filters: []string{FilterTag}, Types: []string{"a"}})
	r.AddListing(Listing{Scope: "", Complete: true})
	if len(r.Listings) != 1 {
		t.Fatalf("got %+v, want one scope", r.Listings)
	}
	if got := r.Listings[0]; got.Complete || !slices.Equal(got.Filters, []string{FilterTag}) || !slices.Equal(got.Types, []string{"a"}) {
		t.Errorf("got %+v, want incomplete, filtered by tag, with type a", got)
	}

	var other Report
	other.AddListing(Listing{Scope: "aws/1/r2", Complete: true, Types: []string{"a"}})
	r.Merge(&other)
	r.Finish(nil, nil, false)
	if len(r.Listings) != 2 || r.Listings[0].Scope != "aws/1/r" || r.Listings[1].Scope != "aws/1/r2" {
		t.Errorf("merged and sorted: %+v", r.Listings)
	}
}
