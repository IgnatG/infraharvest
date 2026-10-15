// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/compute/v1"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// * lists cloud_asset and the services that list what Cloud Asset
// Inventory doesn't find; the aliases, whose types cloud_asset lists too,
// only when named.
func TestWildcardServices(t *testing.T) {
	p := &Provider{}
	optIn := p.OptInServices()
	var wildcard []string
	for name := range p.Services() {
		if !slices.Contains(optIn, name) {
			wildcard = append(wildcard, name)
		}
	}
	slices.Sort(wildcard)
	if want := []string{"cloud_asset", "cloudsql", "dns", "iam", "schedulerJobs"}; !slices.Equal(wildcard, want) {
		t.Errorf("services of *: %v, want %v", wildcard, want)
	}
	for _, name := range optIn {
		if _, ok := serviceAliases[name]; !ok {
			t.Errorf("%s is opt-in but no alias", name)
		}
	}
	for name := range removedServices {
		if _, ok := p.Services()[name]; ok {
			t.Errorf("%s is removed, and a service", name)
		}
	}
}

// Every type of every alias is one cloud_asset lists, so the alias lists
// all of it.
func TestAliasTypesAreMapped(t *testing.T) {
	cat := catalogue.MustBuiltin("google")
	for name, types := range serviceAliases {
		if len(types) == 0 {
			t.Errorf("%s: no types", name)
		}
		g := &CloudAssetLister{types: types}
		if got := g.Types(cat); !slices.Equal(got, slices.Sorted(slices.Values(types))) {
			t.Errorf("%s: lists %v of %v: the catalogue maps no asset type to the rest", name, got, types)
		}
	}
	all := (&CloudAssetLister{}).Types(cat)
	if !slices.Contains(all, "google_compute_network") || !slices.Contains(all, "google_storage_bucket") || slices.Contains(all, "google_sql_database") {
		t.Errorf("cloud_asset lists %v", all)
	}
}

// An alias searches for its types' asset types alone, and keeps only
// resources of its types: regional addresses, not global ones, which
// share their asset type.
func TestAliasSearchesItsTypes(t *testing.T) {
	results := []map[string]any{
		asset("compute.googleapis.com/Address", "//compute.googleapis.com/projects/acme-prod/regions/us-central1/addresses/nat", "us-central1", nil),
		asset("compute.googleapis.com/Address", "//compute.googleapis.com/projects/acme-prod/global/addresses/lb", "global", nil),
		asset("compute.googleapis.com/Network", "//compute.googleapis.com/projects/acme-prod/global/networks/main", "global", nil),
	}
	for _, region := range []string{"us-central1", ""} {
		search := &fakeAssetSearch{results: results, perPage: 500}
		server := httptest.NewServer(search)
		t.Setenv(EndpointEnv, server.URL)
		g := &CloudAssetLister{types: serviceAliases["addresses"]}
		g.SetContext(t.Context())
		g.Configure(harvest.ListerConfig{Provider: "google", Settings: map[string]interface{}{"project": "acme-prod", "region": compute.Region{Name: region}}})
		if err := g.List(); err != nil {
			t.Fatal(err)
		}
		server.Close()
		if !reflect.DeepEqual(search.requests, [][]string{{"compute.googleapis.com/Address"}}) {
			t.Errorf("%q: searched %v", region, search.requests)
		}
		var got []string
		for _, r := range g.Resources {
			got = append(got, r.Address.Type+" "+r.Observed.ID)
		}
		var want []string
		if region != "" {
			want = []string{"google_compute_address projects/acme-prod/regions/us-central1/addresses/nat"}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%q: got %v, want %v", region, got, want)
		}
	}
}

// A service infraharvest no longer has fails with the reason.
func TestRemovedServices(t *testing.T) {
	p := &Provider{}
	err := p.Select("sslCertificates", false)
	if err == nil || !strings.Contains(err.Error(), "no longer a service") || !strings.Contains(err.Error(), "Cloud Asset Inventory") {
		t.Errorf("sslCertificates: %v", err)
	}
	if err := p.Select("widgets", false); err == nil || err.Error() != `not a service of google; "infraharvest services google" lists them` {
		t.Errorf("widgets: %v", err)
	}
	if err := p.Select("networks", false); err != nil {
		t.Fatal(err)
	}
	if g, ok := p.Selected().(*CloudAssetLister); !ok || !slices.Equal(g.types, []string{"google_compute_network"}) || g.Name() != "networks" {
		t.Errorf("networks: %#v", p.Selected())
	}
}

// Init takes the project from its arguments or the environment, and looks
// a region other than global up; global needs no call.
func TestInit(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.Query().Get("fields"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/compute/v1/projects/acme/regions/europe-west1" {
			_, _ = w.Write([]byte(`{"name": "europe-west1"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error": {"code": 404, "message": "The resource 'projects/acme/regions/europe-west9' was not found"}}`))
	}))
	defer server.Close()
	t.Setenv(EndpointEnv, server.URL)
	t.Setenv(projectEnv, "")

	p := &Provider{}
	if err := p.Init([]string{"global", "", ""}); err == nil || !strings.Contains(err.Error(), projectEnv) {
		t.Errorf("without a project: %v", err)
	}
	t.Setenv(projectEnv, "acme")
	if err := p.Init([]string{"global", "", ""}); err != nil || p.projectName != "acme" || p.region.Name != "" || len(paths) != 0 {
		t.Errorf("global: %v, %+v, calls %v", err, p, paths)
	}
	if err := p.Init([]string{"europe-west1", "acme", "beta"}); err != nil || p.region.Name != "europe-west1" || p.Name() != "google-beta" {
		t.Errorf("europe-west1: %v, %+v", err, p)
	}
	if !slices.Equal(paths, []string{"/compute/v1/projects/acme/regions/europe-west1?name"}) {
		t.Errorf("calls %v", paths)
	}
	err := p.Init([]string{"europe-west9", "acme", ""})
	if err == nil || !strings.Contains(err.Error(), "europe-west9") || !strings.Contains(err.Error(), "acme") {
		t.Errorf("unknown region: %v", err)
	}
}

// google-beta, which modules use, targets the import's project and region.
func TestModuleProviders(t *testing.T) {
	p := &Provider{projectName: "acme-prod", providerType: "beta"}
	p.region.Name = "europe-west1"
	if _, ok := p.ProviderBlock()["provider"].(map[string]interface{})["google-beta"]; !ok {
		t.Errorf("provider data: %v", p.ProviderBlock())
	}
	beta, ok := p.ModuleProviders()["google-beta"]
	if !ok || beta.Source != "hashicorp/google-beta" || beta.Config["project"] != "acme-prod" || beta.Config["region"] != "europe-west1" {
		t.Errorf("module providers: %+v", p.ModuleProviders())
	}
	if attribute, block, prefix := p.DefaultTags(); attribute != "labels" || block != "default_labels" || prefix != "goog-" || !p.DefaultTagsArgument() {
		t.Errorf("default tags: %s %s %s", attribute, block, prefix)
	}
}
