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

// The provider hands the listers what reports the types the import
// leaves out, as it hands them its other settings.
func TestSelectPassesUnwantedTypes(t *testing.T) {
	p := &Provider{projectName: "acme"}
	p.SetUnwantedTypes(unwantedOnly(sqlDatabaseType))
	if err := p.Select(cloudSQLService, false); err != nil {
		t.Fatal(err)
	}
	settings := p.Selected().Settings()
	if wants(settings, sqlDatabaseType) || !wants(settings, dnsRecordSetType) {
		t.Errorf("databases wanted %v, record sets %v; want false, true", wants(settings, sqlDatabaseType), wants(settings, dnsRecordSetType))
	}
}

// A lister whose only type the import leaves out whatever is listed, as
// cloudsql's databases, a call per instance, and dns's record sets, a
// call per zone, asks for nothing, not even for the parents.
func TestListersSkipTheCallsOfUnwantedTypes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lister harvest.Lister
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
		forgetSQLInstances(t)

		tc.lister.Configure(harvest.ListerConfig{Provider: "google", Settings: listerSettings{
			region:   compute.Region{Name: tc.region},
			project:  "acme",
			unwanted: unwantedOnly(tc.typ),
		}.args()})
		tc.lister.SetContext(t.Context())
		if err := tc.lister.List(); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		server.Close()
		if len(asked) != 0 || len(tc.lister.Listed()) != 0 {
			t.Errorf("%s: asked for %v and listed %d, want neither", tc.name, asked, len(tc.lister.Listed()))
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
	settings := g.Settings()
	settings[unwantedKey] = unwantedOnly("google_compute_instance", "google_compute_disk", "google_compute_region_disk")
	g.Configure(harvest.ListerConfig{Provider: "google", Settings: settings})
	if err := g.List(); err != nil {
		t.Fatal(err)
	}
	if instances.requests() != 0 {
		t.Errorf("got %d instance lists, want none", instances.requests())
	}
	if len(g.Resources) == 0 {
		t.Error("listed nothing")
	}
	for _, r := range g.Resources {
		if _, ok := r.Observed.Attributes[bootDiskAttribute]; ok {
			t.Errorf("%s: attributes %v", r.Observed.ID, r.Observed.Attributes)
		}
	}
}
