// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal || google

package providers

import (
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
