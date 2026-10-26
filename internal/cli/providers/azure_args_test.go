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
	withAzapi, err := config.Load(file)
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
			planner := azureFlags(flags, &options)
			if err := flags.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			if tc.file != nil {
				if err := tc.file.Apply(flags, "azure"); err != nil {
					t.Fatal(err)
				}
			}
			p, err := planner(context.Background(), importer.Options{ResourceGroup: "rg"})
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
				for _, c := range p.calls(importer.Options{ResourceGroup: "rg"}, scope) {
					got = append(got, c.args)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("args: got %q, want %q", got, tc.want)
			}
		})
	}
}
