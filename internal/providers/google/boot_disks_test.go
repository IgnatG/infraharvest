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
)

// fakeInstances answers the project acme-prod's aggregated instance list
// with pages, one page per request, and records the fields each request
// asked for. fail, while above 0, fails that many requests first.
type fakeInstances struct {
	pages []map[string]any
	fail  int

	mu     sync.Mutex
	fields []string
}

func (f *fakeInstances) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	failing := f.fail > 0
	if failing {
		f.fail--
	}
	f.fields = append(f.fields, r.URL.Query().Get("fields"))
	f.mu.Unlock()
	if failing {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Required 'compute.instances.list' permission","status":"PERMISSION_DENIED"}}`))
		return
	}
	page := 0
	if token := r.URL.Query().Get("pageToken"); token != "" {
		if err := json.Unmarshal([]byte(token), &page); err != nil {
			http.Error(w, "bad token", http.StatusBadRequest)
			return
		}
	}
	body := map[string]any{"items": f.pages[page]}
	if page+1 < len(f.pages) {
		next, _ := json.Marshal(page + 1)
		body["nextPageToken"] = string(next)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeInstances) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fields)
}

// computeAndAssets serves search from Cloud Asset Inventory and
// instances from Compute Engine, as the endpoint EndpointEnv names, with
// none of the searches and instance lists of other tests kept.
func computeAndAssets(t *testing.T, search *fakeAssetSearch, instances *fakeInstances) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/compute/v1/projects/acme-prod/aggregated/instances" {
			instances.ServeHTTP(w, r)
			return
		}
		search.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	t.Setenv(EndpointEnv, server.URL)
	forgetSearches(t)
	bootDiskLists.forget()
	t.Cleanup(bootDiskLists.forget)
}

const computeURL = "https://www.googleapis.com/compute/v1/"

// bootDiskAssets are an instance web, which boots from the disk web and
// has the disk data attached; an instance db, which boots from a regional
// disk; and a disk that boots an instance the search didn't find.
var bootDiskAssets = []map[string]any{
	asset("compute.googleapis.com/Instance", "//compute.googleapis.com/projects/acme-prod/zones/us-central1-a/instances/web", "us-central1-a", nil),
	asset("compute.googleapis.com/Instance", "//compute.googleapis.com/projects/acme-prod/zones/us-central1-b/instances/db", "us-central1-b", nil),
	asset("compute.googleapis.com/Disk", "//compute.googleapis.com/projects/acme-prod/zones/us-central1-a/disks/web", "us-central1-a", nil),
	asset("compute.googleapis.com/Disk", "//compute.googleapis.com/projects/acme-prod/zones/us-central1-a/disks/data", "us-central1-a", nil),
	asset("compute.googleapis.com/Disk", "//compute.googleapis.com/projects/acme-prod/regions/us-central1/disks/db-boot", "us-central1", nil),
	asset("compute.googleapis.com/Disk", "//compute.googleapis.com/projects/acme-prod/zones/us-central1-c/disks/new", "us-central1-c", nil),
}

// bootDiskPages list the instances of bootDiskAssets over two pages, with
// an instance the search didn't find yet, which boots from the disk new.
var bootDiskPages = []map[string]any{
	{
		"zones/us-central1-a": map[string]any{"instances": []map[string]any{{
			"name": "web",
			"disks": []map[string]any{
				{"source": computeURL + "projects/acme-prod/zones/us-central1-a/disks/web", "boot": true},
				{"source": computeURL + "projects/acme-prod/zones/us-central1-a/disks/data"},
			},
		}}},
		"zones/europe-west1-b": map[string]any{"warning": map[string]any{"code": "NO_RESULTS_ON_PAGE"}},
	},
	{
		"zones/us-central1-b": map[string]any{"instances": []map[string]any{{
			"name":  "db",
			"disks": []map[string]any{{"source": computeURL + "projects/acme-prod/regions/us-central1/disks/db-boot", "boot": true}},
		}}},
		"zones/us-central1-c": map[string]any{"instances": []map[string]any{{
			"name":  "new",
			"disks": []map[string]any{{"source": computeURL + "projects/acme-prod/zones/us-central1-c/disks/new", "boot": true}},
		}}},
	},
}

// A disk that boots an instance the search found records the instance,
// and the catalogue leaves it out by default, as the instance manages it
// in its boot_disk block; a data disk, and the boot disk of an instance
// the search didn't find, stay in. One list, page by page, asks for no
// more than it needs.
func TestCloudAssetMarksBootDisks(t *testing.T) {
	instances := &fakeInstances{pages: bootDiskPages}
	computeAndAssets(t, &fakeAssetSearch{results: bootDiskAssets, perPage: 500}, instances)

	got := byID(listAssetsIn(t, nil, "us-central1"))
	cat := catalogue.MustBuiltin("google")
	for id, want := range map[string]string{
		"projects/acme-prod/zones/us-central1-a/disks/web":     "projects/acme-prod/zones/us-central1-a/instances/web",
		"projects/acme-prod/regions/us-central1/disks/db-boot": "projects/acme-prod/zones/us-central1-b/instances/db",
		"projects/acme-prod/zones/us-central1-a/disks/data":    "",
		"projects/acme-prod/zones/us-central1-c/disks/new":     "",
		"projects/acme-prod/zones/us-central1-a/instances/web": "",
		"projects/acme-prod/zones/us-central1-b/instances/db":  "",
	} {
		r, ok := got[id]
		if !ok {
			t.Errorf("%s isn't listed: %v", id, got)
			continue
		}
		a := r.Observed.Attributes
		if a[bootDiskOfAttribute] != want || (a[bootDiskAttribute] == "true") != (want != "") {
			t.Errorf("%s: attributes %v, want the boot disk of %q", id, a, want)
		}
		if skipped := cat.Skipped(r.Address.Type, r.Observed.ID, a) != ""; skipped != (want != "") {
			t.Errorf("%s (%s): left out by default %v", id, r.Address.Type, skipped)
		}
	}
	if instances.requests() != 2 {
		t.Errorf("got %d requests, want the list's 2 pages", instances.requests())
	}
	for _, fields := range instances.fields {
		if fields != bootDiskFields {
			t.Errorf("fields %q, want %q", fields, bootDiskFields)
		}
	}
}

// The imports of a project's regions share one list of its instances,
// also when they list at once.
func TestCloudAssetListsBootDisksOncePerProject(t *testing.T) {
	instances := &fakeInstances{pages: bootDiskPages[:1]}
	computeAndAssets(t, &fakeAssetSearch{results: bootDiskAssets, perPage: 500}, instances)

	regions := []string{"", "us-central1", "europe-west1", "us-central1"}
	errs := make([]error, len(regions))
	var wg sync.WaitGroup
	for i, region := range regions {
		g := assetLister(t, nil, region)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = g.List()
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("%q: %v", regions[i], err)
		}
	}
	if instances.requests() != 1 {
		t.Errorf("got %d instance lists, want 1", instances.requests())
	}
}

// The import of global, which lists no disks, doesn't list the instances.
func TestCloudAssetListsNoInstancesForGlobal(t *testing.T) {
	instances := &fakeInstances{pages: bootDiskPages}
	computeAndAssets(t, &fakeAssetSearch{results: bootDiskAssets, perPage: 500}, instances)
	listAssetsIn(t, nil, "")
	if instances.requests() != 0 {
		t.Errorf("got %d instance lists, want none", instances.requests())
	}
}

// A project whose search found disks but no instances, or instances but
// no disks, has no boot disks to find, so its instances aren't listed.
func TestCloudAssetListsNoInstancesWithoutBoth(t *testing.T) {
	for name, results := range map[string][]map[string]any{
		"disks":     bootDiskAssets[2:],
		"instances": bootDiskAssets[:2],
	} {
		t.Run(name, func(t *testing.T) {
			instances := &fakeInstances{pages: bootDiskPages}
			computeAndAssets(t, &fakeAssetSearch{results: results, perPage: 500}, instances)
			for _, r := range listAssetsIn(t, nil, "us-central1") {
				if _, ok := r.Observed.Attributes[bootDiskAttribute]; ok {
					t.Errorf("%s: attributes %v", r.Observed.ID, r.Observed.Attributes)
				}
			}
			if instances.requests() != 0 {
				t.Errorf("got %d instance lists, want none", instances.requests())
			}
		})
	}
}

// When the instances can't be listed, or a zone can't be reached, every
// disk may be a boot disk: each records that it isn't known, and the
// catalogue leaves it out by default, while the rest of the listing
// stands. A failed list isn't kept: the next import lists again.
func TestCloudAssetBootDisksUnknown(t *testing.T) {
	unreachable := []map[string]any{{
		"zones/us-central1-a": bootDiskPages[0]["zones/us-central1-a"],
		"zones/us-central1-f": map[string]any{"warning": map[string]any{"code": "UNREACHABLE"}},
	}}
	for name, instances := range map[string]*fakeInstances{
		"failed":      {pages: bootDiskPages, fail: 1},
		"unreachable": {pages: unreachable},
	} {
		t.Run(name, func(t *testing.T) {
			computeAndAssets(t, &fakeAssetSearch{results: bootDiskAssets, perPage: 500}, instances)
			cat := catalogue.MustBuiltin("google")
			got := listAssetsIn(t, nil, "us-central1")
			if len(got) != len(bootDiskAssets) {
				t.Fatalf("got %v", byID(got))
			}
			for _, r := range got {
				a := r.Observed.Attributes
				disk := strings.Contains(r.Observed.ID, "/disks/")
				if (a[bootDiskAttribute] == "unknown") != disk || a[bootDiskOfAttribute] != "" {
					t.Errorf("%s: attributes %v", r.Observed.ID, a)
				}
				if skipped := cat.Skipped(r.Address.Type, r.Observed.ID, a) != ""; skipped != disk {
					t.Errorf("%s: left out by default %v", r.Observed.ID, skipped)
				}
			}
		})
	}

	instances := &fakeInstances{pages: bootDiskPages, fail: 1}
	computeAndAssets(t, &fakeAssetSearch{results: bootDiskAssets, perPage: 500}, instances)
	listAssetsIn(t, nil, "us-central1")
	again := byID(listAssetsIn(t, nil, "us-central1"))
	if a := again["projects/acme-prod/zones/us-central1-a/disks/web"].Observed.Attributes; a[bootDiskAttribute] != "true" {
		t.Errorf("after a failure: attributes %v", a)
	}
	if a := again["projects/acme-prod/zones/us-central1-a/disks/data"].Observed.Attributes; a[bootDiskAttribute] != "" {
		t.Errorf("after a failure, the data disk: attributes %v", a)
	}
}

func TestComputeID(t *testing.T) {
	for url, want := range map[string]string{
		computeURL + "projects/acme-prod/zones/us-central1-a/disks/web":                                 "projects/acme-prod/zones/us-central1-a/disks/web",
		"https://compute.googleapis.com/compute/v1/projects/acme-prod/regions/us-central1/disks/shared": "projects/acme-prod/regions/us-central1/disks/shared",
		"":          "",
		"disks/web": "",
	} {
		if got := computeID(url); got != want {
			t.Errorf("%q: got %q, want %q", url, got, want)
		}
	}
}
