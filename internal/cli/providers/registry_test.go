// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal

package providers

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/importer"
)

func TestEveryProviderFileIsRegistered(t *testing.T) {
	files, err := filepath.Glob("provider_cmd_*.go")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, f := range files {
		want = append(want, strings.TrimSuffix(strings.TrimPrefix(f, "provider_cmd_"), ".go"))
	}

	if got := Names(); !slices.Equal(got, want) {
		t.Errorf("registered providers:\n got %v\nwant %v", got, want)
	}
}

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

func TestEveryProviderHasCommandAndConstructor(t *testing.T) {
	constructors := providerConstructors()
	if len(constructors) != len(registry) {
		t.Errorf("%d constructors for %d providers: names must be unique", len(constructors), len(registry))
	}
	for _, name := range Names() {
		if cmd, runE := Command(name, importer.Options{}); cmd == nil || runE == nil {
			t.Errorf("%s: every registered provider needs an import command, and what runs it", name)
		}
	}
	if New("no-such-provider") != nil {
		t.Error("New of an unknown provider must be nil")
	}
}

func TestRegisterProviderRejectsDuplicates(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("want a panic when a provider is registered twice")
		}
	}()
	register("aws", nil, nil)
}

// providerConstructors maps each provider's Terraform name to its constructor.
func providerConstructors() map[string]func() harvest.Provider {
	constructors := make(map[string]func() harvest.Provider)
	for _, name := range Names() {
		newProvider := registry[name].newProvider
		constructors[newProvider().Name()] = newProvider
	}
	return constructors
}

// HashiCorp's own providers, which Terraform finds as hashicorp/<name>.
// Every other provider must name its registry source, or terraform init
// looks for hashicorp/<name> and fails.
var hashicorpProviders = []string{"aws", "azurerm", "google", "google-beta"}

func TestEveryProviderHasARegistrySource(t *testing.T) {
	for name, newProvider := range providerConstructors() {
		if slices.Contains(hashicorpProviders, name) {
			continue
		}
		source := newProvider().Profile().Source
		if source == "" {
			t.Errorf("%s: not a HashiCorp provider, so its profile needs a Source", name)
			continue
		}
		if strings.HasPrefix(source, "hashicorp/") || strings.Count(source, "/") != 1 || strings.HasSuffix(source, "/") {
			t.Errorf("%s: Source is %q, want <namespace>/<type> outside hashicorp/", name, source)
		}
	}
}
