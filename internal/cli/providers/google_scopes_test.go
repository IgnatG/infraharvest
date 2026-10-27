// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal || google

package providers

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/internal/importer"
)

// The google command needs one of the flags its profile names: a run
// that sets none, or sets one empty, says which.
func TestGoogleCheckNeedsAProjectFlag(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "say which projects to import: --projects, --organization or --folders"},
		{[]string{"--projects=", "--organization="}, "say which projects to import: --projects, --organization or --folders"},
		{[]string{"--projects=p"}, ""},
		{[]string{"--organization=123"}, ""},
		{[]string{"--folders=456"}, ""},
	} {
		flags := pflag.NewFlagSet("google", pflag.ContinueOnError)
		options := importer.Options{}
		check, _ := googleFlags(flags, &options)
		if err := flags.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		got := ""
		if err := check(options); err != nil {
			got = err.Error()
		}
		if got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.args, got, tc.want)
		}
	}
}

// A profile that needs no flag needs nothing set, and one that needs a
// flag the command doesn't have counts it as unset: neither panics.
func TestUnsetNeeds(t *testing.T) {
	flags := pflag.NewFlagSet("google", pflag.ContinueOnError)
	flags.StringSlice("projects", nil, "")
	flags.String("organization", "", "")
	if err := flags.Parse([]string{"--organization=123"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		needs []string
		want  string
	}{
		{nil, ""},
		{[]string{"missing"}, "--missing"},
		{[]string{"projects"}, "--projects"},
		{[]string{"projects", "missing"}, "--projects or --missing"},
		{[]string{"missing", "organization"}, ""},
	} {
		if got := unsetNeeds(flags, tc.needs); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.needs, got, tc.want)
		}
	}
}

// Several projects need a layout that keeps them apart: {account}, or the
// {provider}/{service} the plan puts each project into. One project, or
// none named yet, needs neither.
func TestCheckProjectsApart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pattern  string
		projects []string
		want     string
	}{
		{"one project", "{out}/{provider}/", []string{"p1"}, ""},
		{"none named", "{out}/{provider}/", nil, ""},
		{"by account", importer.DefaultLayout, []string{"p1", "p2"}, ""},
		{"by service", "{out}/{provider}/{service}/", []string{"p1", "p2"}, ""},
		{"the provider default", "", []string{"p1", "p2"}, ""},
		{"shared", "{out}/{provider}/{region}/", []string{"p1", "p2"}, `--layout "{out}/{provider}/{region}/" needs {account} to keep several projects apart`},
	} {
		got := ""
		if err := checkProjectsApart(tc.pattern, tc.projects); err != nil {
			got = err.Error()
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	// The command's check says the same, once a project flag is set.
	flags := pflag.NewFlagSet("google", pflag.ContinueOnError)
	options := importer.Options{}
	check, _ := googleFlags(flags, &options)
	if err := flags.Parse([]string{"--projects=p1,p2"}); err != nil {
		t.Fatal(err)
	}
	options.Layout = "{out}/{provider}/{region}/"
	if err := check(options); err == nil || !strings.Contains(err.Error(), "several projects apart") {
		t.Errorf("check: got %v, want the layout refused", err)
	}
}

// Without --organization or --folders, the plan imports the projects of
// --projects, region by region, without reaching the cloud: each call
// reads the region, the project and, with --beta, google-beta; a layout
// with {provider}/{service} gets the project and region put in.
func TestGooglePlan(t *testing.T) {
	type want struct {
		args   []string
		layout string
		where  string
	}
	for _, tc := range []struct {
		name     string
		flags    []string
		layout   string
		projects []string
		want     map[string][]want
		err      string
	}{
		{
			name: "projects and regions", flags: []string{"--regions=global,europe-west1"},
			layout: importer.DefaultLayout, projects: []string{"p1", "p2"},
			want: map[string][]want{
				"p1": {{[]string{"global", "p1", ""}, importer.DefaultLayout, "project p1, region global"}, {[]string{"europe-west1", "p1", ""}, importer.DefaultLayout, "project p1, region europe-west1"}},
				"p2": {{[]string{"global", "p2", ""}, importer.DefaultLayout, "project p2, region global"}, {[]string{"europe-west1", "p2", ""}, importer.DefaultLayout, "project p2, region europe-west1"}},
			},
		},
		{
			name: "beta, by service", flags: []string{"--beta"},
			layout: "{out}/{provider}/{service}/", projects: []string{"p1"},
			want: map[string][]want{
				"p1": {{[]string{"global", "p1", "beta"}, "{out}/{provider}/p1/{service}/global/", "project p1, region global"}},
			},
		},
		{
			name: "projects that would share roots", layout: "{out}/{provider}/{region}/", projects: []string{"p1", "p2"},
			err: "needs {account} to keep several projects apart",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := pflag.NewFlagSet("google", pflag.ContinueOnError)
			options := importer.Options{}
			_, planner := googleFlags(flags, &options)
			if err := flags.Parse(tc.flags); err != nil {
				t.Fatal(err)
			}
			options.Layout, options.Projects = tc.layout, tc.projects
			p, err := planner(context.Background(), options)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("got %v, want an error about %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.kind != "project" || !slices.Equal(p.scopes, tc.projects) {
				t.Errorf("kind %q, scopes %v; want project, %v", p.kind, p.scopes, tc.projects)
			}
			for _, project := range p.scopes {
				calls := p.calls(options, project)
				wants := tc.want[project]
				if len(calls) != len(wants) {
					t.Fatalf("%s: %d calls, want %d", project, len(calls), len(wants))
				}
				for i, w := range wants {
					if c := calls[i]; !slices.Equal(c.args, w.args) || c.options.Layout != w.layout || c.where != w.where {
						t.Errorf("%s: call %d: args %q, layout %q, where %q; want %+v", project, i, c.args, c.options.Layout, c.where, w)
					}
				}
			}
		})
	}
}
