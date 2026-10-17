// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
)

// fakeClusters answers the list of the project acme-prod's GKE clusters
// in every location with response, and records the fields each request
// asked for. fail, while above 0, fails that many requests first.
type fakeClusters struct {
	response map[string]any
	fail     int

	mu     sync.Mutex
	fields []string
}

func (f *fakeClusters) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	failing := f.fail > 0
	if failing {
		f.fail--
	}
	f.fields = append(f.fields, r.URL.Query().Get("fields"))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if failing {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Required \"container.clusters.list\" permission","status":"PERMISSION_DENIED"}}`))
		return
	}
	_ = json.NewEncoder(w).Encode(f.response)
}

func (f *fakeClusters) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fields)
}

// clustersAndAssets serves search from Cloud Asset Inventory and clusters
// from GKE, as the endpoint EndpointEnv names, with none of the searches
// and cluster lists of other tests kept.
func clustersAndAssets(t *testing.T, search *fakeAssetSearch, clusters *fakeClusters) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/projects/acme-prod/locations/-/clusters" {
			clusters.ServeHTTP(w, r)
			return
		}
		search.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv(EndpointEnv, server.URL)
	forgetSearches(t)
	autopilotClusterLists.forget()
	t.Cleanup(autopilotClusterLists.forget)
}

func nodePool(cluster, name, location string) map[string]any {
	return asset("container.googleapis.com/NodePool", cluster+"/nodePools/"+name, location, map[string]any{"parentFullResourceName": cluster})
}

const (
	prodCluster = "//container.googleapis.com/projects/acme-prod/locations/us-central1/clusters/prod"
	edgeCluster = "//container.googleapis.com/projects/acme-prod/locations/europe-west1/clusters/edge"
	devCluster  = "//container.googleapis.com/projects/acme-prod/zones/us-central1-a/clusters/dev"
)

// autopilotAssets are the node pools of a Standard cluster, prod, and of
// two Autopilot clusters: edge, and dev, which is named by its zone.
var autopilotAssets = []map[string]any{
	asset("container.googleapis.com/Cluster", prodCluster, "us-central1", nil),
	asset("container.googleapis.com/Cluster", edgeCluster, "europe-west1", nil),
	nodePool(prodCluster, "default-pool", "us-central1"),
	nodePool(prodCluster, "batch", "us-central1"),
	nodePool(edgeCluster, "default-pool", "europe-west1"),
	nodePool(edgeCluster, "pool-1", "europe-west1"),
	nodePool(devCluster, "default-pool", "us-central1-a"),
}

var autopilotClusters = map[string]any{"clusters": []map[string]any{
	{"name": "prod", "location": "us-central1"},
	{"name": "edge", "location": "europe-west1", "autopilot": map[string]any{"enabled": true}},
	{"name": "dev", "location": "us-central1-a", "autopilot": map[string]any{"enabled": true}},
}}

// The node pools of an Autopilot cluster, its default pool among them,
// record it, and the catalogue leaves them out by default, as GKE
// manages them; a Standard cluster's pools stay in. The imports of a
// project's regions share one list of its clusters, which asks for no
// more than it needs.
func TestCloudAssetMarksAutopilotPools(t *testing.T) {
	clusters := &fakeClusters{response: autopilotClusters}
	clustersAndAssets(t, &fakeAssetSearch{results: autopilotAssets, perPage: 500}, clusters)

	got := byID(listAssetsIn(t, nil, "us-central1"))
	for id, r := range byID(listAssetsIn(t, nil, "europe-west1")) {
		got[id] = r
	}
	cat := catalogue.MustBuiltin("google")
	for id, want := range map[string]bool{
		"projects/acme-prod/locations/us-central1/clusters/prod/nodePools/default-pool":  false,
		"projects/acme-prod/locations/us-central1/clusters/prod/nodePools/batch":         false,
		"projects/acme-prod/locations/europe-west1/clusters/edge/nodePools/default-pool": true,
		"projects/acme-prod/locations/europe-west1/clusters/edge/nodePools/pool-1":       true,
		"projects/acme-prod/zones/us-central1-a/clusters/dev/nodePools/default-pool":     true,
		"projects/acme-prod/locations/us-central1/clusters/prod":                         false,
		"projects/acme-prod/locations/europe-west1/clusters/edge":                        false,
	} {
		r, ok := got[id]
		if !ok {
			t.Errorf("%s isn't listed: %v", id, got)
			continue
		}
		a := r.Observed.Attributes
		if (a[parentAutopilotAttribute] == "true") != want {
			t.Errorf("%s: attributes %v, want an Autopilot cluster's pool %v", id, a, want)
		}
		if skipped := cat.Skipped(r.Address.Type, r.Observed.ID, a) != ""; skipped != want {
			t.Errorf("%s (%s): left out by default %v", id, r.Address.Type, skipped)
		}
	}
	if clusters.requests() != 1 || clusters.fields[0] != autopilotFields {
		t.Errorf("got %d cluster lists, with fields %v; want one, with %q", clusters.requests(), clusters.fields, autopilotFields)
	}
}

// A project whose search found no node pools has no Autopilot pools to
// find, so its clusters aren't listed.
func TestCloudAssetListsNoClustersWithoutNodePools(t *testing.T) {
	clusters := &fakeClusters{response: autopilotClusters}
	clustersAndAssets(t, &fakeAssetSearch{results: autopilotAssets[:2], perPage: 500}, clusters)
	for _, r := range listAssetsIn(t, nil, "us-central1") {
		if _, ok := r.Observed.Attributes[parentAutopilotAttribute]; ok {
			t.Errorf("%s: attributes %v", r.Observed.ID, r.Observed.Attributes)
		}
	}
	if clusters.requests() != 0 {
		t.Errorf("got %d cluster lists, want none", clusters.requests())
	}
}

// When the clusters can't be listed, or a zone's can't, GKE may manage
// any node pool: each records that it isn't known, and the catalogue
// leaves it out by default, while the rest of the listing stands and
// the listing fails in part. The failure is kept: the next import of the
// project doesn't list again.
func TestCloudAssetAutopilotUnknown(t *testing.T) {
	missing := map[string]any{"clusters": autopilotClusters["clusters"], "missingZones": []string{"us-central1-f"}}
	for name, clusters := range map[string]*fakeClusters{
		"failed":  {response: autopilotClusters, fail: 1},
		"missing": {response: missing},
	} {
		t.Run(name, func(t *testing.T) {
			clustersAndAssets(t, &fakeAssetSearch{results: autopilotAssets, perPage: 500}, clusters)
			cat := catalogue.MustBuiltin("google")
			for range 2 {
				g := assetLister(t, nil, "us-central1")
				err := g.List()
				if !harvest.IsPartial(err) || !strings.Contains(err.Error(), "acme-prod") {
					t.Fatalf("error %v, want a partial one naming the project", err)
				}
				if len(g.Resources) != 4 {
					t.Fatalf("got %v", byID(g.Resources))
				}
				for _, r := range g.Resources {
					a := r.Observed.Attributes
					pool := r.Address.Type == "google_container_node_pool"
					if (a[parentAutopilotAttribute] == "unknown") != pool {
						t.Errorf("%s: attributes %v", r.Observed.ID, a)
					}
					if skipped := cat.Skipped(r.Address.Type, r.Observed.ID, a) != ""; skipped != pool {
						t.Errorf("%s: left out by default %v", r.Observed.ID, skipped)
					}
				}
			}
			if clusters.requests() != 1 {
				t.Errorf("got %d cluster lists, want 1", clusters.requests())
			}
		})
	}
}

func TestClusterKey(t *testing.T) {
	for id, want := range map[string]string{
		"projects/acme-prod/locations/us-central1/clusters/prod": "us-central1/prod",
		"projects/acme-prod/zones/us-central1-a/clusters/dev":    "us-central1-a/dev",
		"projects/acme-prod/locations/us-central1":               "",
		"projects/acme-prod/regions/us-central1/clusters/prod":   "",
		"": "",
	} {
		if got := clusterKey(id); got != want {
			t.Errorf("%q: got %q, want %q", id, got, want)
		}
	}
}

// The import of global, which lists no node pools, doesn't list the
// clusters.
func TestCloudAssetListsNoClustersForGlobal(t *testing.T) {
	clusters := &fakeClusters{response: autopilotClusters}
	clustersAndAssets(t, &fakeAssetSearch{results: autopilotAssets, perPage: 500}, clusters)
	listAssetsIn(t, nil, "")
	if clusters.requests() != 0 {
		t.Errorf("got %d cluster lists, want none", clusters.requests())
	}
}
