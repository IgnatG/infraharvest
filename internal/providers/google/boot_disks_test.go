// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/providers/providertest"
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

func (f *fakeInstances) answer(r providertest.Request) providertest.Answer {
	f.mu.Lock()
	failing := f.fail > 0
	if failing {
		f.fail--
	}
	f.fields = append(f.fields, r.Query.Get("fields"))
	f.mu.Unlock()
	if failing {
		return providertest.Status(http.StatusForbidden, `{"error":{"code":403,"message":"Required 'compute.instances.list' permission","status":"PERMISSION_DENIED"}}`)
	}
	page := 0
	if token := r.Query.Get("pageToken"); token != "" {
		if err := json.Unmarshal([]byte(token), &page); err != nil {
			return providertest.Status(http.StatusBadRequest, "bad token")
		}
	}
	body := map[string]any{"items": f.pages[page]}
	if page+1 < len(f.pages) {
		next, _ := json.Marshal(page + 1)
		body["nextPageToken"] = string(next)
	}
	return jsonOf(body)
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
	fakeGoogle(t, func(r providertest.Request) providertest.Answer {
		if r.Path == "/compute/v1/projects/acme-prod/aggregated/instances" {
			return instances.answer(r)
		}
		return search.answer(r)
	})
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
			"selfLink": computeURL + "projects/acme-prod/zones/us-central1-a/instances/web",
			"disks": []map[string]any{
				{"source": computeURL + "projects/acme-prod/zones/us-central1-a/disks/web", "boot": true},
				{"source": computeURL + "projects/acme-prod/zones/us-central1-a/disks/data"},
			},
		}}},
		"zones/europe-west1-b": map[string]any{"warning": map[string]any{"code": "NO_RESULTS_ON_PAGE"}},
	},
	{
		"zones/us-central1-b": map[string]any{"instances": []map[string]any{{
			"selfLink": computeURL + "projects/acme-prod/zones/us-central1-b/instances/db",
			"disks":    []map[string]any{{"source": computeURL + "projects/acme-prod/regions/us-central1/disks/db-boot", "boot": true}},
		}}},
		"zones/us-central1-c": map[string]any{"instances": []map[string]any{{
			"selfLink": computeURL + "projects/acme-prod/zones/us-central1-c/instances/new",
			"disks":    []map[string]any{{"source": computeURL + "projects/acme-prod/zones/us-central1-c/disks/new", "boot": true}},
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
		if fields != instanceFields {
			t.Errorf("fields %q, want %q", fields, instanceFields)
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
	ctx := testRun(t)
	var wg sync.WaitGroup
	for i, region := range regions {
		g := assetLister(t, nil, region)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = g.List(ctx, harvest.Settings{Provider: "google"})
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

// A project whose search found disks but no instances has no boot disks
// to find, so its instances aren't listed.
func TestCloudAssetListsNoInstancesWithoutThem(t *testing.T) {
	instances := &fakeInstances{pages: bootDiskPages}
	computeAndAssets(t, &fakeAssetSearch{results: bootDiskAssets[2:], perPage: 500}, instances)
	for _, r := range listAssetsIn(t, nil, "us-central1") {
		if _, ok := r.Observed.Attributes[bootDiskAttribute]; ok {
			t.Errorf("%s: attributes %v", r.Observed.ID, r.Observed.Attributes)
		}
	}
	if instances.requests() != 0 {
		t.Errorf("got %d instance lists, want none", instances.requests())
	}
}

// An instance that a managed instance group created, which has the
// created-by metadata key, records it, and the catalogue leaves it out by
// default, as the group deletes and recreates it; the others stay in. The
// instances are listed for an import that lists instances and no disks
// too.
func TestCloudAssetMarksInstancesGroupsCreated(t *testing.T) {
	member := map[string]any{
		"selfLink": computeURL + "projects/acme-prod/zones/us-central1-a/instances/web-x1b2",
		"metadata": map[string]any{"items": []map[string]any{{"key": "created-by"}, {"key": "instance-template"}}},
	}
	alone := map[string]any{
		"selfLink": computeURL + "projects/acme-prod/zones/us-central1-a/instances/bastion",
		"metadata": map[string]any{"items": []map[string]any{{"key": "startup-script"}}},
	}
	instances := &fakeInstances{pages: []map[string]any{{"zones/us-central1-a": map[string]any{"instances": []map[string]any{member, alone}}}}}
	computeAndAssets(t, &fakeAssetSearch{results: []map[string]any{
		asset("compute.googleapis.com/Instance", "//compute.googleapis.com/projects/acme-prod/zones/us-central1-a/instances/web-x1b2", "us-central1-a", nil),
		asset("compute.googleapis.com/Instance", "//compute.googleapis.com/projects/acme-prod/zones/us-central1-a/instances/bastion", "us-central1-a", nil),
	}, perPage: 500}, instances)

	got := byID(listAssetsIn(t, nil, "us-central1"))
	cat := catalogue.MustBuiltin("google")
	for id, want := range map[string]bool{
		"projects/acme-prod/zones/us-central1-a/instances/web-x1b2": true,
		"projects/acme-prod/zones/us-central1-a/instances/bastion":  false,
	} {
		r, ok := got[id]
		if !ok {
			t.Errorf("%s isn't listed: %v", id, got)
			continue
		}
		a := r.Observed.Attributes
		if (a[createdByGroupAttribute] == "true") != want {
			t.Errorf("%s: attributes %v, want created by a group %v", id, a, want)
		}
		if skipped := cat.Skipped(r.Address.Type, r.Observed.ID, a) != ""; skipped != want {
			t.Errorf("%s (%s): left out by default %v", id, r.Address.Type, skipped)
		}
	}
	if instances.requests() != 1 || instances.fields[0] != instanceFields {
		t.Errorf("got %d instance lists, with fields %v; want one, with %q", instances.requests(), instances.fields, instanceFields)
	}
}

// When the instances can't be listed for an import that lists no disks,
// the instances are imported, and the listing fails in part.
func TestCloudAssetInstancesUnknown(t *testing.T) {
	instances := &fakeInstances{pages: bootDiskPages, fail: 1}
	computeAndAssets(t, &fakeAssetSearch{results: bootDiskAssets[:2], perPage: 500}, instances)
	g := assetLister(t, nil, "us-central1")
	resources, err := g.List(testRun(t), harvest.Settings{Provider: "google"})
	if !harvest.IsPartial(err) || !strings.Contains(err.Error(), "managed instance groups") {
		t.Fatalf("error %v, want a partial one about managed instance groups", err)
	}
	cat := catalogue.MustBuiltin("google")
	if len(resources) != 2 {
		t.Fatalf("got %v", byID(resources))
	}
	for _, r := range resources {
		if skipped := cat.Skipped(r.Address.Type, r.Observed.ID, r.Observed.Attributes); skipped != "" {
			t.Errorf("%s: left out by default: %s", r.Observed.ID, skipped)
		}
	}
}

// When the instances can't be listed, or a zone can't be reached, every
// disk may be a boot disk: each records that it isn't known, and the
// catalogue leaves it out by default, while the rest of the listing
// stands and the listing fails in part. The failure is kept: the next
// import of the project doesn't list again.
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
			for range 2 {
				g := assetLister(t, nil, "us-central1")
				resources, err := g.List(testRun(t), harvest.Settings{Provider: "google"})
				if !harvest.IsPartial(err) || !strings.Contains(err.Error(), "acme-prod") {
					t.Fatalf("error %v, want a partial one naming the project", err)
				}
				if len(resources) != len(bootDiskAssets) {
					t.Fatalf("got %v", byID(resources))
				}
				for _, r := range resources {
					a := r.Observed.Attributes
					disk := strings.Contains(r.Observed.ID, "/disks/")
					if (a[bootDiskAttribute] == "unknown") != disk || a[bootDiskOfAttribute] != "" {
						t.Errorf("%s: attributes %v", r.Observed.ID, a)
					}
					if skipped := cat.Skipped(r.Address.Type, r.Observed.ID, a) != ""; skipped != disk {
						t.Errorf("%s: left out by default %v", r.Observed.ID, skipped)
					}
				}
			}
			if instances.requests() != 1 {
				t.Errorf("got %d instance lists, want 1", instances.requests())
			}
		})
	}
}

// An instance is named by its own URL, which has the project's ID, so a
// project given by its number still finds the boot disks, which the disk
// sources name by ID too.
func TestBootDisksOfAProjectGivenByNumber(t *testing.T) {
	instances := &fakeInstances{pages: bootDiskPages[:1]}
	fakeGoogle(t, func(r providertest.Request) providertest.Answer {
		if r.Path != "/compute/v1/projects/123456789012/aggregated/instances" {
			return providertest.Status(http.StatusNotFound, "unexpected "+r.Path)
		}
		return instances.answer(r)
	})

	list, err := instancesOnce(testRun(t), "123456789012")
	if err != nil {
		t.Fatal(err)
	}
	boot := list.boot
	if got, want := boot["projects/acme-prod/zones/us-central1-a/disks/web"], "projects/acme-prod/zones/us-central1-a/instances/web"; got != want || len(boot) != 1 {
		t.Errorf("boot disks %v, want web's, booting %s", boot, want)
	}
}

func TestComputeID(t *testing.T) {
	t.Parallel()
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
