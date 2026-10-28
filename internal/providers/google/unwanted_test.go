// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"google.golang.org/api/compute/v1"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// unwantedOnly reports as unwanted the types given, and no other.
func unwantedOnly(types ...string) func(string) bool {
	return func(typ string) bool {
		for _, t := range types {
			if t == typ {
				return true
			}
		}
		return false
	}
}

// A lister whose only type the import leaves out whatever is listed, as
// cloudsql's databases, a call per instance, and dns's record sets, a
// call per zone, asks for nothing, not even for the parents.
func TestListersSkipTheCallsOfUnwantedTypes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lister interface {
			harvest.Lister
			harvest.SettingsTaker[listerSettings]
		}
		typ    string
		region string
	}{
		{"Cloud SQL databases", &CloudSQLLister{}, sqlDatabaseType, "us-central1"},
		{"Cloud DNS record sets", &DNSRecordSetsLister{}, dnsRecordSetType, ""},
	} {
		var mu sync.Mutex
		var asked []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			asked = append(asked, r.URL.Path)
			mu.Unlock()
			http.Error(w, `{"error": {"code": 404, "message": "`+r.URL.Path+`"}}`, http.StatusNotFound)
		}))
		t.Setenv(EndpointEnv, server.URL)

		tc.lister.SetSettings(listerSettings{
			region:  compute.Region{Name: tc.region},
			project: "acme",
		})
		// A run of its own keeps no other test's instance lists.
		listed, err := tc.lister.List(fastRetries(t), harvest.Settings{Provider: "google", Unwanted: unwantedOnly(tc.typ)})
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		server.Close()
		if len(asked) != 0 || len(listed) != 0 {
			t.Errorf("%s: asked for %v and listed %d, want neither", tc.name, asked, len(listed))
		}
	}
}

// A region's import whose instances and disks the import leaves out
// whatever is listed doesn't list the instances to mark the boot disks
// and the instances groups created: it still lists both.
func TestCloudAssetMarksNoUnwantedTypes(t *testing.T) {
	instances := &fakeInstances{pages: bootDiskPages}
	computeAndAssets(t, &fakeAssetSearch{results: bootDiskAssets, perPage: 500}, instances)
	g := assetLister(t, nil, "us-central1")
	unwanted := unwantedOnly("google_compute_instance", "google_compute_disk", "google_compute_region_disk")
	resources, err := g.List(testRun(t), harvest.Settings{Provider: "google", Unwanted: unwanted})
	if err != nil {
		t.Fatal(err)
	}
	if instances.requests() != 0 {
		t.Errorf("got %d instance lists, want none", instances.requests())
	}
	if len(resources) == 0 {
		t.Error("listed nothing")
	}
	for _, r := range resources {
		if _, ok := r.Observed.Attributes[bootDiskAttribute]; ok {
			t.Errorf("%s: attributes %v", r.Observed.ID, r.Observed.Attributes)
		}
	}
}
