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
