// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal || azure

package providers

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/pflag"

	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/importer"
)

// --azapi, from the command line or the configuration file's azure
// settings, adds the provider type to what Init reads; without it, the
// args, which name a call's inventory, are as they were before --azapi.
func TestAzureArgs(t *testing.T) {
	file := filepath.Join(t.TempDir(), "infraharvest.yaml")
	if err := os.WriteFile(file, []byte("version: 1\nproviders:\n  azure:\n    azapi: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withAzapi, err := config.Load(file, Profiles())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		args          []string
		file          *config.File
		subscriptions []string
		want          [][]string
	}{
		{"default", nil, nil, nil, [][]string{{"rg"}}},
		{"subscriptions", []string{"--subscriptions=s1,s2"}, nil, []string{"s1", "s2"}, [][]string{{"rg", "s1"}, {"rg", "s2"}}},
		{"flag", []string{"--azapi"}, nil, nil, [][]string{{"rg", "", "azapi"}}},
		{"configuration file", []string{"--subscriptions=s1"}, withAzapi, []string{"s1"}, [][]string{{"rg", "s1", "azapi"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := pflag.NewFlagSet("azure", pflag.ContinueOnError)
			options := importer.Options{}
			_, planner := azureFlags(flags, &options)
			if err := flags.Parse(append([]string{"--resource-group=rg"}, tc.args...)); err != nil {
				t.Fatal(err)
			}
			if tc.file != nil {
				if err := tc.file.Apply(flags, "azure"); err != nil {
					t.Fatal(err)
				}
			}
			p, err := planner(context.Background(), importer.Options{})
			if err != nil {
				t.Fatal(err)
			}
			scopes := p.scopes
			if len(scopes) == 0 {
				scopes = []string{""}
			}
			if !reflect.DeepEqual(p.scopes, tc.subscriptions) {
				t.Errorf("scopes: got %v, want %v", p.scopes, tc.subscriptions)
			}
			var got [][]string
			for _, scope := range scopes {
				for _, c := range p.calls(importer.Options{}, scope) {
					got = append(got, c.args)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("args: got %q, want %q", got, tc.want)
			}
		})
	}
}

// Several subscriptions of --subscriptions need {account} in the layout,
// which the check says before anything is listed, and the plan again; one
// subscription, or ARM_SUBSCRIPTION_ID's, needs nothing.
func TestAzureSubscriptionsApart(t *testing.T) {
	const shared = "{out}/{provider}/{region}/"
	for _, tc := range []struct {
		name   string
		args   []string
		layout string
		want   string
	}{
		{"ARM_SUBSCRIPTION_ID's", nil, shared, ""},
		{"one subscription", []string{"--subscriptions=s1"}, shared, ""},
		{"several, by account", []string{"--subscriptions=s1,s2"}, importer.DefaultLayout, ""},
		{"several, shared", []string{"--subscriptions=s1,s2"}, shared, `--layout "` + shared + `" needs {account} to keep several subscriptions apart`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := pflag.NewFlagSet("azure", pflag.ContinueOnError)
			options := importer.Options{}
			check, planner := azureFlags(flags, &options)
			if err := flags.Parse(append([]string{"--resource-group=rg"}, tc.args...)); err != nil {
				t.Fatal(err)
			}
			options.Layout = tc.layout
			for name, run := range map[string]func() error{
				"check": func() error { return check(options) },
				"plan": func() error {
					_, err := planner(context.Background(), options)
					return err
				},
			} {
				got := ""
				if err := run(); err != nil {
					got = err.Error()
				}
				if got != tc.want {
					t.Errorf("%s: got %q, want %q", name, got, tc.want)
				}
			}
		})
	}
}
