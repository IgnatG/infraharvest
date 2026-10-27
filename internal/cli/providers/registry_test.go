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

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/config"
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

// Every provider's profile says what its scopes are: what one is called,
// how the user's confirmation of an import describes them, the key of
// their IDs in the configuration file's environments, which the file's
// format has under the provider's command, and its curated modules, if
// any. Every family of curated modules is a provider's.
func TestEveryProfileDescribesItsScope(t *testing.T) {
	var families []string
	for _, p := range Profiles() {
		families = append(families, p.Modules)
	}
	for _, family := range adapters.Providers() {
		if !slices.Contains(families, family) {
			t.Errorf("curated modules of family %q, which no provider's profile names", family)
		}
	}
	for i, p := range Profiles() {
		name := Names()[i]
		if p.Command != name || p.Scope.Noun == "" || p.Scope.Describe == nil {
			t.Errorf("%s: command %q, noun %q: name the command, and what a scope is and how to describe it", name, p.Command, p.Scope.Noun)
		}
		if p.Modules != "" && len(adapters.For(p.Modules)) == 0 {
			t.Errorf("%s: no curated modules of family %q", name, p.Modules)
		}
		const id = "111111111111"
		path := filepath.Join(t.TempDir(), "infraharvest.yaml")
		content := "version: 1\nenvironments:\n  dev: { " + name + ": { " + p.Scope.Field + ": [\"" + id + "\"] } }\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := config.Load(path, Profiles())
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := f.Environments.For(p, id); !slices.Equal(got, []string{"dev"}) {
			t.Errorf("%s: the environment of %s %s is %v, want dev", name, p.Scope.Noun, id, got)
		}
	}
}
