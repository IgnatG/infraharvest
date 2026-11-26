// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/report"
)

// writeListed writes an output directory of an import that listed buckets
// and VPCs in scope, with the listings it says (see report.Listing).
func writeListed(t *testing.T, listings ...report.Listing) string {
	t.Helper()
	out := t.TempDir()
	r := &report.Report{}
	r.Tool = report.Component{Name: "infraharvest", Version: "v0.1.0"}
	r.Engine = report.Component{Name: "terraform", Version: "1.17.0"}
	r.Provider = report.Provider{Source: "hashicorp/aws", Constraint: "~> 6.0"}
	r.Directories = []report.Directory{{
		Path:     "aws/123456789012/eu-west-2",
		Scope:    scope,
		Imported: []report.Resource{{Address: "aws_s3_bucket.logs", ID: "logs-bucket"}},
	}}
	r.AddDiscovered(scope, 2)
	for _, l := range listings {
		r.AddListing(l)
	}
	r.Finish(map[string]int{"aws_s3_bucket": 1, "aws_vpc": 1}, nil, false)
	if err := r.WriteFiles(out); err != nil {
		t.Fatal(err)
	}
	return out
}

// completeOf returns what the inventory says of whether the import listed
// each type and its scope in full.
func completeOf(t *testing.T, doc inventoryDoc) (types map[string]string, scope string) {
	t.Helper()
	show := func(b *bool) string {
		switch {
		case b == nil:
			return "absent"
		case *b:
			return "true"
		}
		return "false"
	}
	types = map[string]string{}
	for _, e := range doc.Coverage.Types {
		types[e.Type] = show(e.Complete)
	}
	if len(doc.Coverage.Scopes) != 1 {
		t.Fatalf("scopes: %+v", doc.Coverage.Scopes)
	}
	return types, show(doc.Coverage.Scopes[0].Complete)
}

// An import that listed everything says so, for the types and the scope,
// and names the types it listed in full even where it found none; one a
// filter touched says which kinds, and which types and scopes it can't
// vouch for; and a report that doesn't say how it listed says nothing.
func TestInventoryCompleteness(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		listings []report.Listing
		types    map[string]string
		scope    string
		complete []string
		filters  *filters
	}{
		{
			name:     "unfiltered",
			listings: []report.Listing{{Scope: scope, Complete: true, Types: []string{"aws_s3_bucket", "aws_sqs_queue", "aws_vpc"}}},
			types:    map[string]string{"aws_s3_bucket": "true", "aws_vpc": "true"},
			scope:    "true", complete: []string{"aws_s3_bucket", "aws_sqs_queue", "aws_vpc"},
			filters: &filters{},
		},
		{
			name:     "--tag",
			listings: []report.Listing{{Scope: scope, Filters: []string{report.FilterTag}}},
			types:    map[string]string{"aws_s3_bucket": "false", "aws_vpc": "false"},
			scope:    "false",
			filters:  &filters{Tag: true},
		},
		{
			name:     "--id, which touches one type",
			listings: []report.Listing{{Scope: scope, Filters: []string{report.FilterID}, Types: []string{"aws_s3_bucket"}}},
			types:    map[string]string{"aws_s3_bucket": "true", "aws_vpc": "false"},
			scope:    "false", complete: []string{"aws_s3_bucket"},
			filters: &filters{ID: true},
		},
		{
			name:     "listed in part",
			listings: []report.Listing{{Scope: scope, Filters: []string{report.FilterPartial}}},
			types:    map[string]string{"aws_s3_bucket": "false", "aws_vpc": "false"},
			scope:    "false",
			filters:  &filters{Partial: true},
		},
		{
			name:     "a service failed",
			listings: []report.Listing{{Scope: scope, Types: []string{"aws_s3_bucket"}}},
			types:    map[string]string{"aws_s3_bucket": "true", "aws_vpc": "false"},
			scope:    "false", complete: []string{"aws_s3_bucket"},
			filters: &filters{},
		},
		{
			name:  "no listings, as in an earlier report",
			types: map[string]string{"aws_s3_bucket": "absent", "aws_vpc": "absent"},
			scope: "absent",
		},
		{
			name:     "another scope listed",
			listings: []report.Listing{{Scope: "aws/123456789012/us-east-1", Complete: true, Types: []string{"aws_s3_bucket", "aws_vpc"}}},
			types:    map[string]string{"aws_s3_bucket": "true", "aws_vpc": "true"},
			scope:    "false",
			filters:  &filters{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			content := build(t, Options{Out: writeListed(t, tc.listings...), Document: DocInventory})
			doc := decodeInventory(t, content)
			types, scopeComplete := completeOf(t, doc)
			for typ, want := range tc.types {
				if types[typ] != want {
					t.Errorf("%s complete: got %s, want %s", typ, types[typ], want)
				}
			}
			if scopeComplete != tc.scope {
				t.Errorf("scope complete: got %s, want %s", scopeComplete, tc.scope)
			}
			if got := doc.Coverage.Scopes[0].CompleteTypes; strings.Join(got, ",") != strings.Join(tc.complete, ",") {
				t.Errorf("complete types: got %v, want %v", got, tc.complete)
			}
			switch {
			case tc.filters == nil && doc.Filters != nil:
				t.Errorf("filters: got %+v, want none", *doc.Filters)
			case tc.filters != nil && (doc.Filters == nil || *doc.Filters != *tc.filters):
				t.Errorf("filters: got %+v, want %+v", doc.Filters, *tc.filters)
			}
			if tc.filters == nil && (strings.Contains(string(content), `"complete`) || strings.Contains(string(content), `"filters"`)) {
				t.Errorf("a report that says nothing of its listing has completeness:\n%s", content)
			}
		})
	}
}

// Only the kinds of filter reach the inventory, never what they match, with
// IDs hashed too.
func TestCompletenessHasKindsOnly(t *testing.T) {
	t.Parallel()
	out := writeListed(t, report.Listing{Scope: scope, Filters: []string{report.FilterTag, report.FilterID}})
	for _, opts := range []Options{{}, {HashKey: []byte("key"), DropAddresses: true}} {
		opts.Out, opts.Document = out, DocInventory
		content := string(build(t, opts))
		if !strings.Contains(content, `"tag": true`) && !strings.Contains(content, `"tag":true`) {
			t.Errorf("the tag filter isn't named:\n%s", content)
		}
		for _, leak := range []string{"planted-tag-value", "planted-id"} {
			if strings.Contains(content, leak) {
				t.Errorf("holds %q:\n%s", leak, content)
			}
		}
	}
}
