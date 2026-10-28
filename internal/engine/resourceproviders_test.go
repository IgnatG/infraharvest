// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// azapiProvider is azapi as the Azure provider configures it for modules
// and for the resources azurerm has no type for.
var azapiProvider = map[string]rootfile.Provider{"azapi": {Name: "azapi", Source: "Azure/azapi", Config: map[string]interface{}{"subscription_id": "00000000-0000-0000-0000-000000000001"}}}

// Only the providers of configured that a type implies count: not the
// root's own, nor google-beta, which import blocks name.
func TestResourceProviders(t *testing.T) {
	t.Parallel()
	configured := map[string]rootfile.Provider{"azapi": {}, "google-beta": {}}
	for _, tc := range []struct {
		imports []rootfile.Import
		want    []string
	}{
		{[]rootfile.Import{{Type: "azurerm_resource_group"}}, nil},
		{[]rootfile.Import{{Type: "azurerm_resource_group"}, {Type: "azapi_resource"}, {Type: "azapi_resource"}}, []string{"azapi"}},
		{[]rootfile.Import{{Type: "google_compute_network", Provider: "google-beta"}}, nil},
	} {
		if got := resourceProviders(tc.imports, configured); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%+v: got %v, want %v", tc.imports, got, tc.want)
		}
	}
}

// A root that imports an azapi_resource requires and configures azapi
// before init, and pins it, once init selected a release, as it pins its
// own provider; a root without one has no azapi.
func TestGenerateRequiresTheProvidersOfItsResources(t *testing.T) {
	t.Parallel()
	azurerm := rootfile.Provider{Name: "azurerm", Source: "hashicorp/azurerm", Version: "~> 5.10", Config: map[string]interface{}{"subscription_id": "00000000-0000-0000-0000-000000000001"}}
	providers, err := rootfile.ProvidersFile(azurerm)
	if err != nil {
		t.Fatal(err)
	}
	config := map[string][]byte{rootfile.VersionsFileName: rootfile.VersionsFile(">= 1.5, < 2.0", azurerm), rootfile.ProvidersFileName: providers}
	const rg = "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/app"
	for name, tc := range map[string]struct {
		imports []rootfile.Import
		azapi   bool
	}{
		"azapi_resource": {[]rootfile.Import{{Type: "azurerm_resource_group", Name: "app", ID: rg}, {Type: "azapi_resource", Name: "fleet", ID: rg + "/providers/Microsoft.ContainerService/fleets/fleet"}}, true},
		"azurerm only":   {[]rootfile.Import{{Type: "azurerm_resource_group", Name: "app", ID: rg}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "azure")
			tf := &fakeTerraform{dir: dir}
			var atInit string
			tf.onInit = func() {
				atInit = readFile(t, dir, rootfile.VersionsFileName) + readFile(t, dir, rootfile.ProvidersFileName)
				writeConfig(t, dir, tfrun.LockFileName, "provider \"registry.terraform.io/hashicorp/azurerm\" {\n  version = \"5.10.0\"\n}\n\nprovider \"registry.terraform.io/azure/azapi\" {\n  version = \"2.13.0\"\n}\n")
			}
			if _, err := Generate(context.Background(), tf, dir, tc.imports, Options{Config: config, ModuleProviders: azapiProvider}); err != nil {
				t.Fatal(err)
			}
			versions, providers := squashed(readFile(t, dir, rootfile.VersionsFileName)), squashed(readFile(t, dir, rootfile.ProvidersFileName))
			if !tc.azapi {
				if strings.Contains(versions+providers+atInit, "azapi") {
					t.Errorf("azapi without an azapi resource:\n%s\n%s", versions, providers)
				}
				return
			}
			if !strings.Contains(squashed(atInit), squashed(`azapi = { source = "Azure/azapi" }`)) || !strings.Contains(squashed(atInit), `provider "azapi"`) {
				t.Errorf("azapi not required and configured before init:\n%s", atInit)
			}
			if !strings.Contains(versions, squashed(`azapi = { source = "Azure/azapi" version = "~> 2.13" }`)) || !strings.Contains(versions, squashed(`azurerm = { source = "hashicorp/azurerm" version = "~> 5.10" }`)) {
				t.Errorf("versions.tf:\n%s", versions)
			}
			if !strings.Contains(providers, squashed(`provider "azapi" { subscription_id = "00000000-0000-0000-0000-000000000001" }`)) || !strings.Contains(providers, `provider "azurerm"`) {
				t.Errorf("providers.tf:\n%s", providers)
			}
			if imports := squashed(readFile(t, dir, rootfile.ImportsFileName)); strings.Contains(imports, "provider =") {
				t.Errorf("import blocks name a provider their type implies:\n%s", imports)
			}
		})
	}
}
