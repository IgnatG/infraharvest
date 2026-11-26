// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// A listing is complete only where nothing narrowed it, and says which kinds
// of filter did, never what they match: the tag value and the ID of the
// filters here are not in what it records. An --id touches its own type; the
// other filters, a failed service and the provider's scopes touch all.
func TestScopeListing(t *testing.T) {
	t.Parallel()
	cat := catalogue.MustBuiltin("aws")
	provider := &fakeProvider{}
	both := []string{"sqs", "s3"}
	all := []string{"*"}
	const scope = "aws/123/eu-west-2"
	saved := sqsListing()
	saved.saved = true
	for _, tc := range []struct {
		name     string
		options  Options
		services []string
		listed   listing
		failures []error
		filters  []string
		complete bool
		types    []string
	}{
		{name: "unfiltered", options: Options{Services: all}, services: both, complete: true, types: []string{"aws_s3_bucket", "aws_sqs_queue"}},
		{name: "--tag", options: Options{Services: all, TagFilters: []string{"team=planted-tag-value"}}, services: both, filters: []string{"tag"}},
		{name: "--where", options: Options{Services: all, WhereFilters: []string{"name=planted-name"}}, services: both, filters: []string{"where"}},
		{name: "--id", options: Options{Services: all, IDFilters: []string{"sqs_queue=planted-id"}}, services: both, filters: []string{"id"}, types: []string{"aws_s3_bucket"}},
		{name: "--services", options: Options{Services: []string{"sqs"}}, services: []string{"sqs"}, filters: []string{"services"}, types: []string{"aws_sqs_queue"}},
		{name: "--skip", options: Options{Services: all, Skip: []string{"s3"}}, services: []string{"sqs"}, filters: []string{"services"}, types: []string{"aws_sqs_queue"}},
		{name: "a service beside the default ones", options: Options{Services: []string{"*", "vpc"}}, services: []string{"sqs", "s3", "vpc"}, complete: true, types: []string{"aws_s3_bucket", "aws_sqs_queue", "aws_vpc"}},
		{name: "--resource-explorer", options: Options{Services: all, ProviderScope: fakeScope{partial: "in part"}}, services: both, filters: []string{"partial"}},
		{name: "--resource-group", options: Options{Services: all, ProviderScope: fakeScope{group: "rg"}}, services: both, filters: []string{"group"}},
		{name: "saved inventory", options: Options{Services: all}, services: both, listed: saved},
		{name: "a failed service", options: Options{Services: all}, services: both, failures: []error{errors.New("service s3: access denied")}, types: []string{"aws_sqs_queue"}},
		{name: "a service failed in part", options: Options{Services: all}, services: both, failures: []error{fmt.Errorf("service sqs: %w", harvest.Partial(errors.New("timeout")))}, types: []string{"aws_s3_bucket"}},
		{name: "a failure of no service", options: Options{Services: all}, services: both, failures: []error{errors.New("listing broke")}},
		{name: "a failure to read tags", options: Options{Services: all}, services: both, failures: []error{fmt.Errorf("%s couldn't read them: denied", tagsFailure)}, complete: true, types: []string{"aws_s3_bucket", "aws_sqs_queue"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &callContext{options: tc.options, cat: cat, services: tc.services, scope: scope}
			got := scopeListing(provider, c, tc.listed, tc.failures)
			slices.Sort(got.Types)
			// Kinds, never what a filter matches.
			if content, err := json.Marshal(got); err != nil || strings.Contains(string(content), "planted") {
				t.Errorf("the listing holds what a filter matches: %s, %v", content, err)
			}
			if got.Scope != scope || got.Complete != tc.complete || !slices.Equal(got.Filters, tc.filters) || !slices.Equal(got.Types, tc.types) {
				t.Errorf("got %+v, want complete %v, filters %v, types %v", got, tc.complete, tc.filters, tc.types)
			}
		})
	}
}
