// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal

package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/terraformutils"
)

// A full build registers every provider_cmd_<name>.go under <name>.
func TestEveryProviderFileIsRegistered(t *testing.T) {
	files, err := filepath.Glob("provider_cmd_*.go")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, f := range files {
		want = append(want, strings.TrimSuffix(strings.TrimPrefix(f, "provider_cmd_"), ".go"))
	}

	if got := registeredProviders(); !slices.Equal(got, want) {
		t.Errorf("registered providers:\n got %v\nwant %v", got, want)
	}
}

// Every provider file must be excludable with -tags minimal.
func TestEveryProviderFileHasBuildConstraint(t *testing.T) {
	files, err := filepath.Glob("provider_cmd_*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(strings.TrimPrefix(f, "provider_cmd_"), ".go")
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "//go:build !minimal || "+name+"\n") {
			t.Errorf("%s: missing //go:build !minimal || %s", f, name)
		}
	}
}

func TestEveryProviderHasCommandAndGenerator(t *testing.T) {
	generators := providerGenerators()
	if len(generators) != len(providerRegistry) {
		t.Errorf("%d generators for %d providers: names must be unique", len(generators), len(providerRegistry))
	}
	if len(providerImporterSubcommands()) != len(providerRegistry) {
		t.Error("every registered provider needs an import command")
	}
}

func TestRegisterProviderRejectsDuplicates(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("want a panic when a provider is registered twice")
		}
	}()
	registerProvider("aws", nil, nil)
}

// providerGenerators maps each provider's Terraform name to its generator.
func providerGenerators() map[string]func() terraformutils.ProviderGenerator {
	generators := make(map[string]func() terraformutils.ProviderGenerator)
	for _, name := range registeredProviders() {
		newProvider := providerRegistry[name].newProvider
		generators[newProvider().GetName()] = newProvider
	}
	return generators
}

// HashiCorp's own providers, which Terraform finds as hashicorp/<name>.
// Every other provider must name its registry source, or terraform init
// looks for hashicorp/<name> and fails.
var hashicorpProviders = []string{"aws", "azuread", "azurerm", "google", "google-beta", "kubernetes", "vault"}

func TestEveryProviderHasARegistrySource(t *testing.T) {
	for name, newProvider := range providerGenerators() {
		if slices.Contains(hashicorpProviders, name) {
			continue
		}
		withSource, ok := newProvider().(terraformutils.ProviderWithSource)
		if !ok {
			t.Errorf("%s: not a HashiCorp provider, so it needs GetSource", name)
			continue
		}
		if source := withSource.GetSource(); strings.HasPrefix(source, "hashicorp/") || strings.Count(source, "/") != 1 || strings.HasSuffix(source, "/") {
			t.Errorf("%s: GetSource is %q, want <namespace>/<type> outside hashicorp/", name, source)
		}
	}
}
