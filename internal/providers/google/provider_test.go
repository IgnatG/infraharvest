// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// The services are cloud_asset and those that list what Cloud Asset
// Inventory doesn't find, and cloud_asset lists every type the catalogue
// maps from it.
func TestServices(t *testing.T) {
	var names []string
	for name := range (&Provider{}).Services() {
		names = append(names, name)
	}
	slices.Sort(names)
	if want := []string{"cloud_asset", "cloudsql", "dns", "iam", "schedulerJobs"}; !slices.Equal(names, want) {
		t.Errorf("services: %v, want %v", names, want)
	}
	all := (&CloudAssetLister{}).Types(catalogue.MustBuiltin("google"))
	if !slices.Contains(all, "google_compute_network") || !slices.Contains(all, "google_storage_bucket") || slices.Contains(all, "google_sql_database") {
		t.Errorf("cloud_asset lists %v", all)
	}
}

// A name that isn't a service, such as networks, which cloud_asset
// lists, fails and says how to find the services.
func TestSelectUnknownService(t *testing.T) {
	p := &Provider{}
	for _, name := range []string{"widgets", "networks", "sslCertificates"} {
		if err := p.Select(name, false); err == nil || err.Error() != `not a service of google; "infraharvest services google" lists them` {
			t.Errorf("Select(%q): %v", name, err)
		}
	}
	if err := p.Select(cloudAssetService, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Selected().(*CloudAssetLister); !ok {
		t.Errorf("cloud_asset: %#v", p.Selected())
	}
}

// Init takes the project from its arguments or the environment, and looks
// a region other than global up; global needs no call.
func TestInit(t *testing.T) {
	regionNames.Clear()
	t.Cleanup(regionNames.Clear)
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
	if err := p.Init(t.Context(), []string{"global", "", ""}); err == nil || !strings.Contains(err.Error(), projectEnv) {
		t.Errorf("without a project: %v", err)
	}
	t.Setenv(projectEnv, "acme")
	if err := p.Init(t.Context(), []string{"global", "", ""}); err != nil || p.projectName != "acme" || p.region.Name != "" || len(paths) != 0 {
		t.Errorf("global: %v, %+v, calls %v", err, p, paths)
	}
	if err := p.Init(t.Context(), []string{"europe-west1", "acme", "beta"}); err != nil || p.region.Name != "europe-west1" || p.Name() != "google-beta" || p.Directory() != "google" {
		t.Errorf("europe-west1: %v, %+v, directory %q", err, p, p.Directory())
	}
	// Another service of the same call asks again: the region is known.
	if err := p.Init(t.Context(), []string{"europe-west1", "acme", ""}); err != nil || p.region.Name != "europe-west1" {
		t.Errorf("europe-west1 again: %v, %+v", err, p)
	}
	if !slices.Equal(paths, []string{"/compute/v1/projects/acme/regions/europe-west1?name"}) {
		t.Errorf("calls %v", paths)
	}
	err := p.Init(t.Context(), []string{"europe-west9", "acme", ""})
	if err == nil || !strings.Contains(err.Error(), "europe-west9") || !strings.Contains(err.Error(), "acme") {
		t.Errorf("unknown region: %v", err)
	}
}

// A project that hasn't enabled the Compute Engine API has its region
// taken as given, when it is shaped like a region's name; any other
// denial still fails.
func TestInitWithoutComputeEngine(t *testing.T) {
	disabled := `{"error": {"code": 403, "message": "Compute Engine API has not been used in project acme before or it is disabled.", "errors": [{"message": "Compute Engine API has not been used in project acme before or it is disabled.", "domain": "usageLimits", "reason": "accessNotConfigured"}], "status": "PERMISSION_DENIED", "details": [{"": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "SERVICE_DISABLED", "domain": "googleapis.com"}]}}`
	denied := `{"error": {"code": 403, "message": "Required 'compute.regions.get' permission for 'projects/acme/regions/europe-west1'", "errors": [{"message": "Required 'compute.regions.get' permission", "domain": "global", "reason": "forbidden"}], "status": "PERMISSION_DENIED"}}`
	serve := func(body string) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(server.Close)
		t.Setenv(EndpointEnv, server.URL)
	}
	serve(disabled)

	p := &Provider{}
	if err := p.Init(t.Context(), []string{"europe-west1", "acme", ""}); err != nil || p.region.Name != "europe-west1" {
		t.Errorf("Compute Engine disabled: %v, region %q", err, p.region.Name)
	}
	if err := p.Init(t.Context(), []string{"europe-west1-b", "acme", ""}); err == nil {
		t.Error("a zone for a region: want an error")
	}
	serve(denied)
	if err := p.Init(t.Context(), []string{"europe-west1", "acme", ""}); err == nil || !strings.Contains(err.Error(), "compute.regions.get") {
		t.Errorf("denied: %v", err)
	}
}

// The region lookup takes the caller's context, so an interrupt stops it.
// Before the fix it ran under a context of its own, which only its
// one-minute timeout ended.
func TestInitStopsWithItsContext(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name": "europe-west1"}`))
	}))
	defer server.Close()
	t.Setenv(EndpointEnv, server.URL)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := (&Provider{}).Init(ctx, []string{"europe-west1", "acme", ""})
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Errorf("got %v after %d calls, want %v before any", err, calls, context.Canceled)
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
	var _ harvest.ProviderWithEffectiveTags = p
	if attribute, effective, prefix := p.EffectiveTags(); attribute != "labels" || effective != "effective_labels" || prefix != "goog-" {
		t.Errorf("effective tags: %s %s %s", attribute, effective, prefix)
	}
	var _ harvest.ProviderWithInheritedArguments = p
	if got := strings.Join(p.InheritedArguments(), ","); got != "project,region,zone" {
		t.Errorf("inherited arguments: %s", got)
	}
}
