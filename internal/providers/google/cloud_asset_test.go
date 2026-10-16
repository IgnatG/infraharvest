// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/cloudasset/v1"
	"google.golang.org/api/compute/v1"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

func TestAssetID(t *testing.T) {
	for _, tt := range []struct{ name, project, want string }{
		{"//compute.googleapis.com/projects/acme-prod/global/networks/main", "projects/123", "projects/acme-prod/global/networks/main"},
		{"//storage.googleapis.com/acme-assets", "projects/123", "acme-assets"},
		// Services that name the project by its number get its ID.
		{"//monitoring.googleapis.com/projects/123/alertPolicies/456", "projects/123", "projects/acme-prod/alertPolicies/456"},
		{"//secretmanager.googleapis.com/projects/123/secrets/db", "projects/123", "projects/acme-prod/secrets/db"},
		// Only the project's own number, and only where a project goes.
		{"//compute.googleapis.com/projects/acme-prod/global/images/projects/123/x", "projects/123", "projects/acme-prod/global/images/projects/123/x"},
		{"//monitoring.googleapis.com/projects/999/alertPolicies/456", "projects/123", "projects/999/alertPolicies/456"},
		{"//pubsub.googleapis.com/projects/acme-prod/topics/t", "", "projects/acme-prod/topics/t"},
		{"//cloudresourcemanager.googleapis.com", "", ""},
	} {
		if got := assetID(tt.name, tt.project, "acme-prod"); got != tt.want {
			t.Errorf("%s (%s): got %q, want %q", tt.name, tt.project, got, tt.want)
		}
	}
}

func TestInRegion(t *testing.T) {
	for _, tt := range []struct {
		location, region string
		want             bool
	}{
		{"us-central1", "us-central1", true},
		{"us-central1-a", "us-central1", true},
		{"US-CENTRAL1", "us-central1", true},
		{"us-central10", "us-central1", false},
		{"europe-west1", "us-central1", false},
		{"global", "us-central1", false},
		{"global", "", true},
		{"us", "", true},
		{"nam4", "", true},
		{"", "", true},
		{"us-central1", "", false},
		{"northamerica-northeast1-b", "", false},
	} {
		if got := inRegion(tt.location, tt.region); got != tt.want {
			t.Errorf("%q in %q: got %v", tt.location, tt.region, got)
		}
	}
}

// A resource is named after its own name. Before, a Pub/Sub topic, whose
// display name is its full name, was named
// projects_acme_prod_topics_orders.
func TestAssetName(t *testing.T) {
	for _, tt := range []struct{ displayName, id, want string }{
		{"projects/acme-prod/topics/orders", "projects/acme-prod/topics/orders", "orders"},
		{"projects/acme-prod/subscriptions/orders-worker", "projects/acme-prod/subscriptions/orders-worker", "orders-worker"},
		{"main", "projects/acme-prod/global/networks/main", "main"},
		{"High latency / 5 min", "projects/acme-prod/alertPolicies/987", "High latency / 5 min"},
		{"", "projects/acme-prod/regions/us-central1/disks/shared", "shared"},
		{"", "acme-assets", "acme-assets"},
	} {
		if got := assetName(tt.displayName, tt.id); got != tt.want {
			t.Errorf("assetName(%q, %q): got %q, want %q", tt.displayName, tt.id, got, tt.want)
		}
	}
}

func TestSnakeCase(t *testing.T) {
	for in, want := range map[string]string{"email": "email", "uniqueId": "unique_id", "dnsName": "dns_name"} {
		if got := snakeCase(in); got != want {
			t.Errorf("%s: got %s", in, got)
		}
	}
}

// fakeAssetSearch serves searchAllResources from results, a page at a
// time, and records the asset types each request asked for. Requests for
// one of reject fail as Cloud Asset Inventory fails for a type it can't
// search.
type fakeAssetSearch struct {
	results []map[string]any
	reject  []string
	perPage int

	mu       sync.Mutex
	requests [][]string
	masks    []string
}

func (f *fakeAssetSearch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/v1/projects/acme-prod:searchAllResources") {
		http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	types := q["assetTypes"]
	f.mu.Lock()
	f.requests = append(f.requests, types)
	f.masks = append(f.masks, q.Get("readMask"))
	f.mu.Unlock()
	for _, t := range types {
		if slices.Contains(f.reject, t) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Invalid asset type","status":"INVALID_ARGUMENT"}}`))
			return
		}
	}
	var matching []map[string]any
	for _, res := range f.results {
		if slices.Contains(types, res["assetType"].(string)) {
			matching = append(matching, res)
		}
	}
	start := 0
	if token := q.Get("pageToken"); token != "" {
		if err := json.Unmarshal([]byte(token), &start); err != nil {
			http.Error(w, "bad token", http.StatusBadRequest)
			return
		}
	}
	end := min(start+f.perPage, len(matching))
	page := map[string]any{"results": matching[start:end]}
	if end < len(matching) {
		next, _ := json.Marshal(end)
		page["nextPageToken"] = string(next)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

func asset(assetType, name, location string, extra map[string]any) map[string]any {
	res := map[string]any{"assetType": assetType, "name": name, "location": location, "project": "projects/123456789012"}
	for k, v := range extra {
		res[k] = v
	}
	return res
}

var searchResults = []map[string]any{
	asset("compute.googleapis.com/Network", "//compute.googleapis.com/projects/acme-prod/global/networks/main", "global", map[string]any{"displayName": "main"}),
	asset("compute.googleapis.com/Network", "//compute.googleapis.com/projects/acme-prod/global/networks/default", "global", map[string]any{"displayName": "default"}),
	asset("compute.googleapis.com/Subnetwork", "//compute.googleapis.com/projects/acme-prod/regions/us-central1/subnetworks/app", "us-central1", map[string]any{"displayName": "app", "labels": map[string]string{"team": "web"}}),
	asset("compute.googleapis.com/Disk", "//compute.googleapis.com/projects/acme-prod/zones/us-central1-a/disks/data", "us-central1-a", nil),
	asset("compute.googleapis.com/Disk", "//compute.googleapis.com/projects/acme-prod/regions/us-central1/disks/shared", "us-central1", nil),
	asset("compute.googleapis.com/Subnetwork", "//compute.googleapis.com/projects/acme-prod/regions/europe-west1/subnetworks/eu", "europe-west1", nil),
	asset("storage.googleapis.com/Bucket", "//storage.googleapis.com/acme-assets", "us", map[string]any{"labels": map[string]string{"team": "web"}}),
	asset("iam.googleapis.com/ServiceAccount", "//iam.googleapis.com/projects/acme-prod/serviceAccounts/104355798712345678901", "global", map[string]any{
		"displayName":          "deployer",
		"additionalAttributes": map[string]any{"email": "deployer@acme-prod.iam.gserviceaccount.com", "uniqueId": "104355798712345678901"},
	}),
	asset("monitoring.googleapis.com/AlertPolicy", "//monitoring.googleapis.com/projects/123456789012/alertPolicies/987", "global", map[string]any{"displayName": "High latency"}),
	asset("pubsub.googleapis.com/Topic", "//pubsub.googleapis.com/projects/acme-prod/topics/gone", "global", map[string]any{"state": "DELETED"}),
}

// listAssets lists region (global for "") of the project acme-prod from
// search, with cat (the built-in catalogue for nil), with none of the
// searches of other tests kept.
func listAssets(t *testing.T, search *fakeAssetSearch, cat *catalogue.Catalogue, region string) []harvest.Resource {
	t.Helper()
	server := httptest.NewServer(search)
	t.Cleanup(server.Close)
	t.Setenv(EndpointEnv, server.URL)
	forgetSearches(t)
	return listAssetsIn(t, cat, region)
}

// forgetSearches drops the searches the process has kept, now and when
// the test ends, so that a test's search doesn't reach another's, which
// a server on the same port would otherwise give it.
func forgetSearches(t *testing.T) {
	t.Helper()
	forget := func() {
		searches.Lock()
		searches.byKey = map[string]*projectSearch{}
		searches.Unlock()
	}
	forget()
	t.Cleanup(forget)
}

// listAssetsIn lists region (global for "") of the project acme-prod from
// the endpoint EndpointEnv names, with cat (the built-in catalogue for
// nil).
func listAssetsIn(t *testing.T, cat *catalogue.Catalogue, region string) []harvest.Resource {
	t.Helper()
	g := assetLister(t, cat, region)
	if err := g.List(); err != nil {
		t.Fatal(err)
	}
	return g.Resources
}

// assetLister is a lister of region (global for "") of the project
// acme-prod, with cat (the built-in catalogue for nil), and the test's
// context.
func assetLister(t *testing.T, cat *catalogue.Catalogue, region string) *CloudAssetLister {
	t.Helper()
	g := &CloudAssetLister{}
	g.SetContext(t.Context())
	args := map[string]interface{}{"project": "acme-prod", "region": compute.Region{Name: region}}
	if cat != nil {
		args["catalogue"] = cat
	}
	g.Configure(harvest.ListerConfig{Provider: "google", Settings: args})
	return g
}

func byID(resources []harvest.Resource) map[string]harvest.Resource {
	m := map[string]harvest.Resource{}
	for _, r := range resources {
		m[r.Observed.ID] = r
	}
	return m
}

// One search, page by page, for the catalogue's asset types; the global
// import takes what isn't in a region, with the IDs Terraform imports.
func TestCloudAssetListsTheProject(t *testing.T) {
	search := &fakeAssetSearch{results: searchResults, perPage: 3}
	got := byID(listAssets(t, search, nil, ""))

	want := map[string]string{
		"projects/acme-prod/global/networks/main":    "google_compute_network",
		"projects/acme-prod/global/networks/default": "google_compute_network",
		"acme-assets": "google_storage_bucket",
		"projects/acme-prod/serviceAccounts/104355798712345678901": "google_service_account",
		"projects/acme-prod/alertPolicies/987":                     "google_monitoring_alert_policy",
	}
	if len(got) != len(want) {
		t.Errorf("got %d resources, want %d: %v", len(got), len(want), got)
	}
	for id, typ := range want {
		if r, ok := got[id]; !ok || r.Address.Type != typ {
			t.Errorf("%s: got %+v, want a %s", id, r.Address, typ)
		}
	}
	if len(search.requests) < 2 {
		t.Errorf("want the search's pages, got %d requests", len(search.requests))
	}
	if !reflect.DeepEqual(search.requests[0], catalogue.MustBuiltin("google").InventoryTypes()) || search.masks[0] != searchReadMask {
		t.Errorf("search: asset types %v, read mask %q", search.requests[0], search.masks[0])
	}

	sa := got["projects/acme-prod/serviceAccounts/104355798712345678901"]
	if a := sa.Observed.Attributes; a["asset.email"] != "deployer@acme-prod.iam.gserviceaccount.com" || a["asset.unique_id"] == "" || a["asset.project"] != "acme-prod" || sa.RawName != "deployer" {
		t.Errorf("service account: name %q, attributes %v", sa.RawName, a)
	}
	cat := catalogue.MustBuiltin("google")
	if id, ok, _ := cat.ImportID(sa.Address.Type, sa.Observed.ID, sa.Observed.Attributes); !ok || id != "projects/acme-prod/serviceAccounts/deployer@acme-prod.iam.gserviceaccount.com" {
		t.Errorf("service account import ID: %q", id)
	}
	if bucket := got["acme-assets"]; harvest.AttributeTags(bucket.Observed.Attributes)["team"] != "web" {
		t.Errorf("bucket labels: %v", bucket.Observed.Attributes)
	}
	def := got["projects/acme-prod/global/networks/default"]
	if cat.Skipped(def.Address.Type, def.Observed.ID, def.Observed.Attributes) == "" {
		t.Error("the default network isn't left out by default")
	}
	if main := got["projects/acme-prod/global/networks/main"]; cat.Skipped(main.Address.Type, main.Observed.ID, main.Observed.Attributes) != "" {
		t.Error("a network of the project's is left out by default")
	}
}

// A region's import takes what is in the region and its zones, with the
// zonal and regional types of one asset type apart.
func TestCloudAssetListsARegion(t *testing.T) {
	got := byID(listAssets(t, &fakeAssetSearch{results: searchResults, perPage: 500}, nil, "us-central1"))
	want := map[string]string{
		"projects/acme-prod/regions/us-central1/subnetworks/app": "google_compute_subnetwork",
		"projects/acme-prod/zones/us-central1-a/disks/data":      "google_compute_disk",
		"projects/acme-prod/regions/us-central1/disks/shared":    "google_compute_region_disk",
	}
	if len(got) != len(want) {
		t.Errorf("got %v", got)
	}
	for id, typ := range want {
		if got[id].Address.Type != typ {
			t.Errorf("%s: got %q, want %s", id, got[id].Address.Type, typ)
		}
	}
}

// The imports of a project's regions and of global share one search, each
// taking what is in its region, also when they list at once. Before, each
// searched the whole project again: N regions made N+1 searches.
func TestCloudAssetSearchesOncePerProject(t *testing.T) {
	search := &fakeAssetSearch{results: searchResults, perPage: 500}
	server := httptest.NewServer(search)
	t.Cleanup(server.Close)
	t.Setenv(EndpointEnv, server.URL)
	forgetSearches(t)

	regions := []string{"", "us-central1", "europe-west1", "us-central1", ""}
	listers := make([]*CloudAssetLister, len(regions))
	errs := make([]error, len(regions))
	var wg sync.WaitGroup
	for i, region := range regions {
		listers[i] = assetLister(t, nil, region)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = listers[i].List()
		}()
	}
	wg.Wait()

	got := make([][]harvest.Resource, len(regions))
	for i, g := range listers {
		if errs[i] != nil {
			t.Fatalf("%q: %v", regions[i], errs[i])
		}
		got[i] = g.Resources
	}
	if len(search.requests) != 1 {
		t.Errorf("got %d searches, want 1", len(search.requests))
	}
	for i, want := range []int{5, 3, 1, 3, 5} {
		if len(got[i]) != want {
			t.Errorf("%q: got %d resources, want %d: %v", regions[i], len(got[i]), want, byID(got[i]))
		}
	}
}

// A search that fails isn't kept: the next import searches again.
func TestCloudAssetSearchesAgainAfterAFailure(t *testing.T) {
	search := &fakeAssetSearch{results: searchResults, perPage: 500}
	var fail sync.Mutex
	failing := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail.Lock()
		f := failing
		failing = false
		fail.Unlock()
		if f {
			http.Error(w, "unavailable", http.StatusForbidden)
			return
		}
		search.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv(EndpointEnv, server.URL)
	forgetSearches(t)

	if err := assetLister(t, nil, "").List(); err == nil {
		t.Fatal("the failed search succeeded")
	}
	if got := listAssetsIn(t, nil, ""); len(got) != 5 || len(search.requests) != 1 {
		t.Errorf("after a failure: got %d resources from %d searches", len(got), len(search.requests))
	}
}

// When the search rejects the asset types, as it does for one it can't
// search, it searches them one by one and leaves out what it rejects;
// --catalogue entries map types the built-in catalogue doesn't.
func TestCloudAssetSearchesTypeByType(t *testing.T) {
	dir := t.TempDir()
	overlay := "provider: google\ntypes:\n  google_widget:\n    inventory: { type: example.googleapis.com/Widget }\n"
	if err := os.WriteFile(filepath.Join(dir, "widgets.yaml"), []byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := catalogue.Load("google", dir)
	if err != nil {
		t.Fatal(err)
	}
	search := &fakeAssetSearch{results: searchResults, perPage: 500, reject: []string{"example.googleapis.com/Widget"}}
	got := byID(listAssets(t, search, cat, ""))
	if _, ok := got["projects/acme-prod/global/networks/main"]; !ok || len(got) != 5 {
		t.Errorf("got %v", got)
	}
	if n := len(cat.InventoryTypes()); len(search.requests) != n+1 {
		t.Errorf("got %d requests, want the rejected one and one per type, %d", len(search.requests), n+1)
	}
}

// A node pool records its cluster's labels, from the same search, so
// that the catalogue leaves out the pools of a cluster Cloud Composer
// created, as it leaves out the cluster.
func TestCloudAssetRecordsTheParentsLabels(t *testing.T) {
	cluster := "//container.googleapis.com/projects/acme-prod/locations/us-central1/clusters/us-central1-pipelines-1a2b3c4d-gke"
	results := []*cloudasset.ResourceSearchResult{
		{AssetType: "container.googleapis.com/Cluster", Name: cluster, Location: "us-central1", Labels: map[string]string{"goog-composer-version": "composer-2-9-7-airflow-2-9-3"}},
		{AssetType: "container.googleapis.com/NodePool", Name: cluster + "/nodePools/default-pool", Location: "us-central1", ParentFullResourceName: cluster},
		{AssetType: "container.googleapis.com/NodePool", Name: "//container.googleapis.com/projects/acme-prod/locations/us-central1/clusters/prod/nodePools/default-pool", Location: "us-central1", ParentFullResourceName: "//container.googleapis.com/projects/acme-prod/locations/us-central1/clusters/prod"},
	}
	cat := catalogue.MustBuiltin("google")
	got := byID(assetResources(results, cat, "google", "acme-prod", "us-central1"))
	composer, ok := got["projects/acme-prod/locations/us-central1/clusters/us-central1-pipelines-1a2b3c4d-gke/nodePools/default-pool"]
	prod, prodOK := got["projects/acme-prod/locations/us-central1/clusters/prod/nodePools/default-pool"]
	if !ok || !prodOK {
		t.Fatalf("got %v", got)
	}
	if a := composer.Observed.Attributes; a["asset.parent_labels.goog-composer-version"] != "composer-2-9-7-airflow-2-9-3" {
		t.Errorf("the Composer cluster's pool: attributes %v", a)
	}
	if harvest.AttributeTags(composer.Observed.Attributes)["goog-composer-version"] != "" {
		t.Error("the cluster's labels are the pool's")
	}
	if cat.Skipped(composer.Address.Type, composer.Observed.ID, composer.Observed.Attributes) == "" {
		t.Error("the Composer cluster's pool isn't left out by default")
	}
	if prod.Address.Type != "google_container_node_pool" || cat.Skipped(prod.Address.Type, prod.Observed.ID, prod.Observed.Attributes) != "" {
		t.Errorf("the prod cluster's pool: %+v, attributes %v", prod.Address, prod.Observed.Attributes)
	}
}

// A resource records its description, which tells the load balancers GKE
// creates for LoadBalancer Services, named a and a hash, from the user's.
func TestCloudAssetRecordsTheDescription(t *testing.T) {
	results := []*cloudasset.ResourceSearchResult{
		{AssetType: "compute.googleapis.com/ForwardingRule", Name: "//compute.googleapis.com/projects/acme-prod/regions/us-central1/forwardingRules/a0123456789abcdef0123456789abcdef", Location: "us-central1", Description: `{"kubernetes.io/service-name":"default/web"}`},
		{AssetType: "compute.googleapis.com/ForwardingRule", Name: "//compute.googleapis.com/projects/acme-prod/regions/us-central1/forwardingRules/web", Location: "us-central1"},
	}
	cat := catalogue.MustBuiltin("google")
	got := assetResources(results, cat, "google", "acme-prod", "us-central1")
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	for _, r := range got {
		skipped := cat.Skipped(r.Address.Type, r.Observed.ID, r.Observed.Attributes) != ""
		if gke := r.Observed.Attributes["asset.description"] != ""; skipped != gke {
			t.Errorf("%s: attributes %v, left out %v", r.Observed.ID, r.Observed.Attributes, skipped)
		}
	}
}

func TestCloudAssetLeavesOutUnmapped(t *testing.T) {
	results := []*cloudasset.ResourceSearchResult{
		{AssetType: "compute.googleapis.com/BackendBucket", Name: "//compute.googleapis.com/projects/acme-prod/regions/us-central1/backendBuckets/b", Location: "global"},
		{AssetType: "example.googleapis.com/Widget", Name: "//example.googleapis.com/projects/acme-prod/widgets/w", Location: "global"},
	}
	if got := assetResources(results, catalogue.MustBuiltin("google"), "google", "acme-prod", ""); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}

// cloud_asset is one of the services * lists, and gets the import's
// catalogue.
func TestCloudAssetIsInTheWildcard(t *testing.T) {
	p := &Provider{}
	if _, ok := p.Services()[cloudAssetService]; !ok {
		t.Fatal("cloud_asset isn't a service")
	}
	if _, optIn := harvest.Provider(p).(harvest.ProviderWithOptInServices); optIn {
		t.Error("google has opt-in services, which * leaves out")
	}
	cat := catalogue.MustBuiltin("google")
	p.SetCatalogue(cat)
	if err := p.Select(cloudAssetService, false); err != nil {
		t.Fatal(err)
	}
	if p.Selected().Settings()["catalogue"] != cat {
		t.Error("the service doesn't get the import's catalogue")
	}
}
