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
	azureprovider "github.com/IgnatG/infraharvest/internal/providers/azure"
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
		{name: "unfiltered", options: Options{Services: all}, services: both, types: []string{"aws_s3_bucket", "aws_sqs_queue"}},
		{name: "--tag", options: Options{Services: all, TagFilters: []string{"team=planted-tag-value"}}, services: both, filters: []string{"tag"}},
		{name: "--where", options: Options{Services: all, WhereFilters: []string{"name=planted-name"}}, services: both, filters: []string{"where"}},
		{name: "--id", options: Options{Services: all, IDFilters: []string{"sqs_queue=planted-id"}}, services: both, filters: []string{"id"}, types: []string{"aws_s3_bucket"}},
		{name: "--services", options: Options{Services: []string{"sqs"}}, services: []string{"sqs"}, filters: []string{"services"}, types: []string{"aws_sqs_queue"}},
		{name: "--skip", options: Options{Services: all, Skip: []string{"s3"}}, services: []string{"sqs"}, filters: []string{"services"}, types: []string{"aws_sqs_queue"}},
		{name: "a service beside the default ones", options: Options{Services: []string{"*", "vpc"}}, services: []string{"sqs", "s3", "vpc"}, types: []string{"aws_s3_bucket", "aws_sqs_queue", "aws_vpc"}},
		{name: "--resource-explorer", options: Options{Services: all, ProviderScope: fakeScope{partial: "in part"}}, services: both, filters: []string{"partial"}},
		{name: "--resource-group", options: Options{Services: all, ProviderScope: fakeScope{group: "rg"}}, services: both, filters: []string{"group"}},
		{name: "saved inventory", options: Options{Services: all}, services: both, listed: saved},
		{name: "a failed service", options: Options{Services: all}, services: both, failures: []error{errors.New("service s3: access denied")}, types: []string{"aws_sqs_queue"}},
		{name: "a service failed in part", options: Options{Services: all}, services: both, failures: []error{fmt.Errorf("service sqs: %w", harvest.Partial(errors.New("timeout")))}, types: []string{"aws_s3_bucket"}},
		{name: "a failure of no service", options: Options{Services: all}, services: both, failures: []error{errors.New("listing broke")}},
		{name: "a failure to read tags", options: Options{Services: all}, services: both, failures: []error{fmt.Errorf("%s couldn't read them: denied", tagsFailure)}, types: []string{"aws_s3_bucket", "aws_sqs_queue"}},
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

// A scope is complete only when its complete types are every type of the
// import: a type of a service that is a child, is listed in part, can't be
// imported or isn't wanted is not among them.
func TestScopeListingIsCompleteOnlyWhenEveryTypeIs(t *testing.T) {
	t.Parallel()
	cat := catalogue.MustBuiltin("aws")
	provider := &fakeProvider{}
	unwantedInstances := func(typ string) bool { return typ == "aws_instance" }
	for _, tc := range []struct {
		name     string
		provider harvest.Provider
		services []string
		unwanted func(string) bool
		want     bool
	}{
		// aws_instance is the only type of the ec2_instance service.
		{name: "every type listed in full", provider: provider, services: []string{"ec2_instance"}, want: true},
		{name: "a child of another type", provider: provider, services: []string{"s3"}, want: false},
		{name: "a type the provider lists in part", provider: &partialProvider{Provider: provider, partial: []string{"aws_instance"}}, services: []string{"ec2_instance"}, want: false},
		{name: "a type the import doesn't list", provider: provider, services: []string{"ec2_instance"}, unwanted: unwantedInstances, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &callContext{options: Options{Services: []string{"*"}}, cat: cat, services: tc.services, scope: "aws/1/eu-west-2", unwanted: tc.unwanted}
			if got := scopeListing(tc.provider, c, listing{}, nil); got.Complete != tc.want {
				t.Errorf("got complete %v with types %v, want %v", got.Complete, got.Types, tc.want)
			}
		})
	}
}

// diagnostic_setting lists from what the other services listed: when one
// of them failed, its settings are not complete, though its own service
// didn't fail.
func TestScopeListingDependentServiceNeedsTheOthersToHaveListed(t *testing.T) {
	t.Parallel()
	az := &azureprovider.Provider{}
	const diagnostic = "azurerm_monitor_diagnostic_setting"
	services := append(Services(az), "diagnostic_setting")
	list := func(failures ...error) []string {
		c := &callContext{options: Options{Services: []string{"*", "diagnostic_setting"}}, cat: catalogue.MustBuiltin("azurerm"), services: services, scope: "azure/sub"}
		return scopeListing(az, c, listing{}, failures).Types
	}
	if !slices.Contains(list(), diagnostic) {
		t.Errorf("no failure: want %s complete", diagnostic)
	}
	if slices.Contains(list(errors.New("service role_assignment: denied")), diagnostic) {
		t.Errorf("another service failed: %s is complete", diagnostic)
	}
}
