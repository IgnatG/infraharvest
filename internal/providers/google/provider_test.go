// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"errors"
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

// The services are cloud_asset, those that list what Cloud Asset
// Inventory doesn't find and the query service, and cloud_asset lists
// every type the catalogue maps from it.
func TestServices(t *testing.T) {
	t.Parallel()
	var names []string
	for name := range (&Provider{}).Services() {
		names = append(names, name)
	}
	slices.Sort(names)
	if want := []string{"cloud_asset", "cloudsql", "dns", "iam", "query", "schedulerJobs"}; !slices.Equal(names, want) {
		t.Errorf("services: %v, want %v", names, want)
	}
	all := (&CloudAssetLister{}).Types(catalogue.MustBuiltin("google"))
	if !slices.Contains(all, "google_compute_network") || !slices.Contains(all, "google_storage_bucket") || slices.Contains(all, "google_sql_database") {
		t.Errorf("cloud_asset lists %v", all)
	}
}

// A name that isn't a service, such as networks, which cloud_asset
// lists, fails and says how to find the services.
func TestListerOfAnUnknownService(t *testing.T) {
	t.Parallel()
	p := &Provider{}
	for _, name := range []string{"widgets", "networks", "sslCertificates"} {
		if _, err := p.Lister(name); err == nil || err.Error() != `not a service of google; "infraharvest services google" lists them` {
			t.Errorf("Lister(%q): %v", name, err)
		}
	}
	lister, err := p.Lister(cloudAssetService)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lister.(*CloudAssetLister); !ok {
		t.Errorf("cloud_asset: %#v", lister)
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
	if err := p.Init(t.Context(), []string{"europe-west1", "acme", "beta"}); err != nil || p.region.Name != "europe-west1" || p.Name() != "google-beta" || p.Profile().Command != "google" {
		t.Errorf("europe-west1: %v, %+v, directory %q", err, p, p.Profile().Command)
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

// The counterpart of the root's provider, which modules use besides it,
// targets the import's project and region: google-beta for google, and
// google for google-beta.
func TestModuleProviders(t *testing.T) {
	t.Parallel()
	ga := &Provider{projectName: "acme-prod"}
	ga.region.Name = "europe-west1"
	if got := ga.ModuleProviders(); len(got) != 1 || got["google-beta"].Source != "hashicorp/google-beta" || got["google-beta"].Config["project"] != "acme-prod" || got["google-beta"].Config["region"] != "europe-west1" {
		t.Errorf("module providers of google: %+v", got)
	}
	p := &Provider{projectName: "acme-prod", providerType: "beta"}
	p.region.Name = "europe-west1"
	if _, ok := p.ProviderBlock()["provider"].(map[string]interface{})["google-beta"]; !ok {
		t.Errorf("provider data: %v", p.ProviderBlock())
	}
	if got := p.ModuleProviders(); len(got) != 1 || got["google"].Source != "hashicorp/google" || got["google"].Config["project"] != "acme-prod" || got["google"].Config["region"] != "europe-west1" {
		t.Errorf("module providers of google-beta: %+v", got)
	}
	profile := p.Profile()
	if got, want := profile.DefaultTags, (&harvest.DefaultTags{Attribute: "labels", Block: "default_labels", Argument: true, ReservedPrefix: "goog-"}); !reflect.DeepEqual(got, want) {
		t.Errorf("default tags: %+v", got)
	}
	if got, want := profile.EffectiveTags, (&harvest.EffectiveTags{Attribute: "labels", Effective: "effective_labels", ReservedPrefix: "goog-"}); !reflect.DeepEqual(got, want) {
		t.Errorf("effective tags: %+v", got)
	}
	if got := strings.Join(profile.InheritedArguments, ","); got != "project,region,zone" {
		t.Errorf("inherited arguments: %s", got)
	}
}

// What Terraform's list resources return is in a region's import by the
// region, zone or location of its identity, and in the global import when
// it has none, or a multi-region.
func TestInQueryScope(t *testing.T) {
	t.Parallel()
	regional := &Provider{region: compute.Region{Name: "europe-west2"}}
	global := &Provider{}
	for _, tt := range []struct {
		identity         map[string]string
		regional, global bool
	}{
		{map[string]string{"name": "net", "project": "p"}, false, true},
		{map[string]string{"name": "sub", "region": "europe-west2"}, true, false},
		{map[string]string{"name": "vm", "zone": "europe-west2-b"}, true, false},
		{map[string]string{"name": "vm", "zone": "us-central1-a"}, false, false},
		{map[string]string{"name": "svc", "location": "europe-west2"}, true, false},
		{map[string]string{"name": "data", "location": "EU"}, false, true},
	} {
		if got := regional.InQueryScope(tt.identity); got != tt.regional {
			t.Errorf("%v: in europe-west2 %v", tt.identity, got)
		}
		if got := global.InQueryScope(tt.identity); got != tt.global {
			t.Errorf("%v: global %v", tt.identity, got)
		}
	}
	if regional.QueryConfig() != nil {
		t.Errorf("config %v", regional.QueryConfig())
	}
}
