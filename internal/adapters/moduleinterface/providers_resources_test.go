// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package moduleinterface

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hashicorp/go-version"

	"github.com/infraharvest/infraharvest-cli/internal/adapters"
)

// readFixture reads the interface of the module in testdata/<name>, packed
// as a tar.gz of its repository.
func readFixture(t *testing.T, name string) *Interface {
	t.Helper()
	root := filepath.Join("testdata", name)
	files := map[string]string{}
	err := filepath.WalkDir(root, func(file string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		files[name+"-abc123/"+filepath.ToSlash(rel)] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	iface, err := Read(bytes.NewReader(moduleArchive(t, files)), "")
	if err != nil {
		t.Fatal(err)
	}
	return iface
}

// registry answers as engine.ProviderVersion would from the releases of
// each provider source, and records the sources asked about.
type registry struct {
	releases map[string][]string
	asked    []string
}

func (r *registry) newest(source string, constraints []version.Constraints) (*version.Version, error) {
	r.asked = append(r.asked, source)
	var newest *version.Version
	for _, s := range r.releases[source] {
		v := version.Must(version.NewVersion(s))
		accepted := true
		for _, c := range constraints {
			accepted = accepted && c.Check(v)
		}
		if accepted && (newest == nil || v.GreaterThan(newest)) {
			newest = v
		}
	}
	if newest == nil {
		return nil, errors.New("no release meets the constraints")
	}
	return newest, nil
}

// Read records the required providers in each form, and the resources of
// the module and of the local modules it calls: not those of a registry
// module it calls, nor of an example.
func TestReadRecordsProvidersAndResources(t *testing.T) {
	t.Parallel()
	providers := readFixture(t, "providers")
	wantProviders := map[string]Provider{
		"azurerm": {Source: "hashicorp/azurerm", Version: ">= 4.60.0, < 5.0.0"},
		"azapi":   {Source: "Azure/azapi", Version: "~> 2.12"},
		"random":  {Source: "hashicorp/random"},
		"google":  {Version: "~> 7.0"},
	}
	if !reflect.DeepEqual(providers.Providers, wantProviders) {
		t.Errorf("providers: got %+v, want %+v", providers.Providers, wantProviders)
	}
	if providers.Resources != nil {
		t.Errorf("resources of a module without any: got %q", providers.Resources)
	}

	identity := readFixture(t, "identity")
	wantResources := []string{
		"azurerm_federated_identity_credential.this",
		"azurerm_user_assigned_identity.this",
		"module.locks.azurerm_management_lock.this",
	}
	if !reflect.DeepEqual(identity.Resources, wantResources) {
		t.Errorf("resources: got %q, want %q", identity.Resources, wantResources)
	}
	if identity.Providers != nil {
		t.Errorf("providers of a module that requires none: got %+v", identity.Providers)
	}
}

// CheckProviders names a provider whose constraint accepts only an older
// major release than the newest, and leaves alone one it accepts a release
// of the newest major of, one the module doesn't require and one it
// requires without a version, which it doesn't look up.
func TestCheckProviders(t *testing.T) {
	t.Parallel()
	providers := readFixture(t, "providers")
	releases := map[string][]string{
		"hashicorp/azurerm": {"4.60.0", "4.70.0", "5.0.1", "5.10.0"},
		"Azure/azapi":       {"2.12.0", "2.15.0"},
		"hashicorp/google":  {"6.49.0", "7.3.0"},
		"hashicorp/random":  {"3.7.2"},
	}
	for _, tc := range []struct {
		name  string
		iface *Interface
		names []string
		held  []string
		asked []string
	}{
		{
			name:  "accepts the newest major",
			iface: providers,
			names: []string{"azapi", "google"},
			asked: []string{"Azure/azapi", "Azure/azapi", "hashicorp/google", "hashicorp/google"},
		},
		{
			name:  "accepts an older release of the newest major",
			iface: &Interface{Providers: map[string]Provider{"azurerm": {Version: ">= 5.0, < 5.1"}}},
			names: []string{"azurerm"},
			asked: []string{"hashicorp/azurerm", "hashicorp/azurerm"},
		},
		{
			name:  "accepts an older major only",
			iface: providers,
			names: []string{"azurerm"},
			held:  []string{"it accepts azurerm `>= 4.60.0, < 5.0.0` only, not 5.x, the newest major release (5.10.0)"},
			asked: []string{"hashicorp/azurerm", "hashicorp/azurerm"},
		},
		{
			name:  "missing provider",
			iface: providers,
			names: []string{"aws"},
		},
		{
			name:  "no constraint",
			iface: providers,
			names: []string{"random"},
		},
		{
			name:  "a constraint that doesn't parse",
			iface: &Interface{Providers: map[string]Provider{"azurerm": {Version: "~> five"}}},
			names: []string{"azurerm"},
			held:  []string{"its constraint on azurerm, `~> five`, doesn't parse"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &registry{releases: releases}
			held, err := CheckProviders(tc.iface, tc.names, r.newest)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(held, tc.held) {
				t.Errorf("held: got %q, want %q", held, tc.held)
			}
			if !reflect.DeepEqual(r.asked, tc.asked) {
				t.Errorf("asked: got %q, want %q", r.asked, tc.asked)
			}
		})
	}
}

// CheckProviders returns the error of a provider it can't look up.
func TestCheckProvidersReturnsLookupErrors(t *testing.T) {
	t.Parallel()
	iface := &Interface{Providers: map[string]Provider{"azurerm": {Source: "acme/azurerm", Version: "~> 1.0"}}}
	r := &registry{}
	if _, err := CheckProviders(iface, []string{"azurerm"}, r.newest); err == nil {
		t.Error("want the lookup's error")
	}
}

// CheckResources names each target the module doesn't declare, with what
// it may have become: a resource of its type, or else of its name, in the
// same module.
func TestCheckResources(t *testing.T) {
	t.Parallel()
	identity := readFixture(t, "identity")
	azapi := readFixture(t, "identity-azapi")
	for _, tc := range []struct {
		name    string
		targets []string
		iface   *Interface
		want    []string
	}{
		{
			name:    "all present",
			targets: []string{"azurerm_user_assigned_identity.this", "module.locks.azurerm_management_lock.this"},
			iface:   identity,
		},
		{
			name:    "one missing",
			targets: []string{"azurerm_user_assigned_identity.this", "azurerm_role_assignment.admin"},
			iface:   identity,
			want:    []string{"no resource azurerm_role_assignment.admin, which the adapter maps onto"},
		},
		{
			name:    "renamed",
			targets: []string{"module.locks.azurerm_management_lock.main"},
			iface:   identity,
			want:    []string{"no resource module.locks.azurerm_management_lock.main, which the adapter maps onto (the module has module.locks.azurerm_management_lock.this)"},
		},
		{
			name:    "type changed",
			targets: adapters.AzureUserAssignedIdentity.Targets,
			iface:   azapi,
			want: []string{
				"no resource azurerm_federated_identity_credential.this, which the adapter maps onto (the module has azapi_resource.this)",
				"no resource azurerm_user_assigned_identity.this, which the adapter maps onto (the module has azapi_resource.this)",
			},
		},
		{
			name:    "no resources recorded",
			targets: []string{"azurerm_user_assigned_identity.this"},
			iface:   &Interface{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := CheckResources(tc.targets, tc.iface); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("problems: got %q, want %q", got, tc.want)
			}
		})
	}
}
