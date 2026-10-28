// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// A lister listing a service with settings of its own provider's, and the
// import's.
type typedLister struct {
	ListerOf[string]
	listed []Resource
	err    error
}

func (l *typedLister) List(ctx context.Context, s Settings) ([]Resource, error) {
	return l.Run(ctx, s, func() error {
		l.Resources = append(l.Resources, l.listed...)
		return l.err
	})
}

// Run gives the lister the context and the settings of the call, and
// returns what it listed with its error, even a partial one, which keeps
// what was listed.
func TestListerOfRun(t *testing.T) {
	t.Parallel()
	partial := Partial(errors.New("a part was refused"))
	l := &typedLister{listed: []Resource{RecordBare("vpc-1", "one", "aws_vpc", "aws")}, err: partial}
	l.SetSettings("eu-west-1")

	got, err := l.List(t.Context(), Settings{Service: "vpc", Provider: "aws", Verbose: true})

	if !errors.Is(err, partial) || !IsPartial(err) {
		t.Errorf("error: got %v, want the lister's partial one", err)
	}
	if len(got) != 1 || got[0].Observed.ID != "vpc-1" {
		t.Errorf("listed: got %v, want vpc-1", got)
	}
	if l.Name() != "vpc" || l.Provider() != "aws" || !l.Verbose() || l.Settings() != "eu-west-1" {
		t.Errorf("accessors: got %q, %q, %v, %q", l.Name(), l.Provider(), l.Verbose(), l.Settings())
	}
}

func TestSettingsWants(t *testing.T) {
	t.Parallel()
	unwanted := Settings{Unwanted: func(typ string) bool { return typ == "aws_iam_access_key" }}
	for _, tc := range []struct {
		name     string
		settings Settings
		types    []string
		want     bool
	}{
		{"every type, when the import leaves none out", Settings{}, []string{"aws_iam_access_key"}, true},
		{"a type it leaves out", unwanted, []string{"aws_iam_access_key"}, false},
		{"another type", unwanted, []string{"aws_iam_user"}, true},
		{"any of types, one wanted", unwanted, []string{"aws_iam_access_key", "aws_iam_user"}, true},
		{"any of types, none wanted", unwanted, []string{"aws_iam_access_key"}, false},
	} {
		if got := tc.settings.WantsAny(tc.types...); got != tc.want {
			t.Errorf("%s: WantsAny(%v) = %v, want %v", tc.name, tc.types, got, tc.want)
		}
		if len(tc.types) == 1 {
			if got := tc.settings.Wants(tc.types[0]); got != tc.want {
				t.Errorf("%s: Wants(%v) = %v, want %v", tc.name, tc.types[0], got, tc.want)
			}
		}
	}
}

// ListerFor gives the lister it makes the provider's settings when it takes
// them, makes a lister that takes none as it is, and fails for a service the
// provider doesn't have, naming the command that lists them.
func TestListerFor(t *testing.T) {
	t.Parallel()
	newLister := func(name string) Lister {
		switch name {
		case "typed":
			return &typedLister{}
		case QueryService:
			return QueryLister{}
		}
		return nil
	}

	typed, err := ListerFor("aws", "typed", newLister, "eu-west-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := typed.(*typedLister).Settings(); got != "eu-west-1" {
		t.Errorf("settings: got %q, want eu-west-1", got)
	}
	if untyped, err := ListerFor("aws", QueryService, newLister, "eu-west-1"); err != nil || untyped != (QueryLister{}) {
		t.Errorf("a lister without settings: got %v, %v", untyped, err)
	}
	_, err = ListerFor("aws", "nothing", newLister, "eu-west-1")
	if err == nil || !strings.Contains(err.Error(), `not a service of aws; "infraharvest services aws" lists them`) {
		t.Errorf("a service it lacks: got %v", err)
	}
}

func TestParseFilters(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		parse func(string) (Filter, error)
		raw   string
		want  Filter
	}{
		{"one id", ParseIDFilter, "aws_vpc=myid", Filter{Service: "aws_vpc", Path: "id", Values: []string{"myid"}}},
		{"several ids, one with a colon", ParseIDFilter, "resource=id1,project:dataset_id", Filter{Service: "resource", Path: "id", Values: []string{"id1", "project:dataset_id"}}},
		{"empty ids are dropped", ParseIDFilter, "vpc=,vpc-1,,vpc-2,", Filter{Service: "vpc", Path: "id", Values: []string{"vpc-1", "vpc-2"}}},
		{"an id with =", ParseIDFilter, "secret=a=b", Filter{Service: "secret", Path: "id", Values: []string{"a=b"}}},
		{"ids, one with =", ParseIDFilter, "secret=a=b,c", Filter{Service: "secret", Path: "id", Values: []string{"a=b", "c"}}},
		{"an identity", ParseIDFilter, "flow_log=id=fl-1,region=eu-west-2", Filter{Service: "flow_log", Path: "id", Values: []string{"id=fl-1,region=eu-west-2"}}},
		{"a tag with values", ParseTagFilter, "env=prod,staging", Filter{Path: "tags.env", Values: []string{"prod", "staging"}}},
		{"a tag without values", ParseTagFilter, "owner", Filter{Path: "tags.owner"}},
		{"a tag with an empty value", ParseTagFilter, "owner=", Filter{Path: "tags.owner", Values: []string{""}}},
		{"an attribute", ParseWhereFilter, "cidr_block=10.0.0.0/16", Filter{Path: "cidr_block", Values: []string{"10.0.0.0/16"}}},
		{"a nested attribute without values", ParseWhereFilter, "labels.env", Filter{Path: "labels.env"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.parse(tc.raw)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%q: got %#v, %v; want %#v", tc.raw, got, err, tc.want)
			}
		})
	}
}

func TestParseFiltersRejects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		parse func(string) (Filter, error)
		raw   string
		want  string
	}{
		{ParseIDFilter, "vpc", `--id "vpc": use <type>=<id>`},
		{ParseIDFilter, "=vpc-1", `--id "=vpc-1": use <type>=<id>`},
		{ParseIDFilter, "vpc=", `--id "vpc=" names no ID`},
		{ParseIDFilter, "vpc=,", `--id "vpc=," names no ID`},
		{ParseTagFilter, "", `--tag "": no key`},
		{ParseTagFilter, "=prod", `--tag "=prod": no key`},
		{ParseWhereFilter, "=x", `--where "=x": no path`},
		{ParseWhereFilter, "id=vpc-1", `--where "id=vpc-1": filter on IDs with --id`},
	} {
		if _, err := tc.parse(tc.raw); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got %v, want an error saying %q", tc.raw, err, tc.want)
		}
	}
}

func TestAttributeFilters(t *testing.T) {
	t.Parallel()
	some := Record("vpc-some", "some", "aws_vpc", "aws", map[string]string{"tags.Name": "some"})
	def := Record("vpc-def", "def", "aws_vpc", "aws", map[string]string{"tags.Name": "default"})
	abc := Record("vpc-abc", "abc", "aws_vpc", "aws", map[string]string{"tags.Abc": ""})

	for _, tc := range []struct {
		name, raw string
		accepted  []Resource
		rejected  []Resource
	}{
		{
			name:     "name and value",
			raw:      "Name=default",
			accepted: []Resource{def},
			rejected: []Resource{some},
		},
		{
			// With no value, the tag only has to exist, even empty.
			name:     "name only",
			raw:      "Abc",
			accepted: []Resource{abc},
			rejected: []Resource{def},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter, err := ParseTagFilter(tc.raw)
			if err != nil {
				t.Fatalf("ParseTagFilter(%q): %v", tc.raw, err)
			}
			for _, resource := range tc.accepted {
				if !filter.Keeps(resource) {
					t.Errorf("%q rejects %s, want it accepted", tc.raw, resource.RawName)
				}
			}
			for _, resource := range tc.rejected {
				if filter.Keeps(resource) {
					t.Errorf("%q accepts %s, want it rejected", tc.raw, resource.RawName)
				}
			}
		})
	}
}

// cleanupResource returns a resource with exactly the given type, name,
// state ID and provider, and no attributes.
func cleanupResource(resourceType, name, stateID, provider string) Resource {
	return Resource{
		Address:  &Address{Type: resourceType},
		Observed: &Observation{ID: stateID},
		RawName:  name,
		Provider: provider,
	}
}

// resourceTypes returns the types of resources, in order.
func resourceTypes(resources []Resource) []string {
	types := make([]string, 0, len(resources))
	for _, resource := range resources {
		types = append(types, resource.Address.Type)
	}
	return types
}

func TestPrune(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		resources []Resource
		filters   []Filter
		wantTypes []string
		wantIDs   []string
	}{
		{
			// Without a provider the type is the service's name. The
			// filter drops type1 and leaves type2 be, and the two are not
			// duplicates although they share an ID and a name.
			name: "resources without a provider",
			resources: []Resource{
				cleanupResource("type1", "", "myid", ""),
				cleanupResource("type2", "", "myid", ""),
			},
			filters:   []Filter{{Service: "type1", Path: "id", Values: []string{"otherId"}}},
			wantTypes: []string{"type2"},
			wantIDs:   []string{"myid"},
		},
		{
			// Only the exact duplicate goes: a role and a group sharing an
			// name and an ID are different resources.
			name: "types sharing a name and an id",
			resources: []Resource{
				cleanupResource("aws_iam_role", "admins", "admins", "aws"),
				cleanupResource("aws_iam_group", "admins", "admins", "aws"),
				cleanupResource("aws_iam_group", "admins", "admins", "aws"),
			},
			filters:   []Filter{{Service: "aws_iam_user", Path: "id", Values: []string{"other"}}},
			wantTypes: []string{"aws_iam_role", "aws_iam_group"},
			wantIDs:   []string{"admins", "admins"},
		},
		{
			// Two APIs' $default stages share a type and a name but not
			// an ID. Before, the second was dropped as a duplicate.
			name: "same name, other id",
			resources: []Resource{
				RecordBare("api1/$default", "$default", "aws_apigatewayv2_stage", "aws"),
				RecordBare("api2/$default", "$default", "aws_apigatewayv2_stage", "aws"),
				RecordBare("api2/$default", "$default", "aws_apigatewayv2_stage", "aws"),
			},
			filters:   []Filter{{Service: "aws_vpc", Path: "id", Values: []string{"other"}}},
			wantTypes: []string{"aws_apigatewayv2_stage", "aws_apigatewayv2_stage"},
			wantIDs:   []string{"api1/$default", "api2/$default"},
		},
		{
			// The ID filter applies; the attribute filter, meant for the
			// listers, must not drop resources that recorded no tags.
			name: "attribute and id filters",
			resources: []Resource{
				RecordBare("vpc1", "vpc1", "aws_vpc", "aws"),
				RecordBare("vpc2", "vpc2", "aws_vpc", "aws"),
			},
			filters:   []Filter{{Path: "tags.Name", Values: []string{"default"}}, {Service: "vpc", Path: "id", Values: []string{"vpc2"}}},
			wantTypes: []string{"aws_vpc"},
			wantIDs:   []string{"vpc2"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pruned := Prune(tc.resources, tc.filters)

			if got := resourceTypes(pruned); !reflect.DeepEqual(got, tc.wantTypes) {
				t.Errorf("types kept: got %v, want %v", got, tc.wantTypes)
			}
			ids := make([]string, 0, len(pruned))
			for _, resource := range pruned {
				ids = append(ids, resource.Observed.ID)
			}
			if !reflect.DeepEqual(ids, tc.wantIDs) {
				t.Errorf("ids kept: got %v, want %v", ids, tc.wantIDs)
			}
		})
	}
}

func TestRecordBare(t *testing.T) {
	t.Parallel()
	r := RecordBare("https://sqs/1/my queue", "my queue", "aws_sqs_queue", "aws")

	for _, field := range []struct{ name, got, want string }{
		{"RawName", r.RawName, "my queue"},
		{"Provider", r.Provider, "aws"},
		{"Address.Type", r.Address.Type, "aws_sqs_queue"},
		{"Observed.ID", r.Observed.ID, "https://sqs/1/my queue"},
	} {
		if field.got != field.want {
			t.Errorf("%s: got %q, want %q", field.name, field.got, field.want)
		}
	}
	if r.Observed.Attributes == nil {
		t.Error("Observed.Attributes: got nil, want an empty map")
	}
}
