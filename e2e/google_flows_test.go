// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || google)

package e2e

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/providers/google"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// useAssetSearch sends the listers' requests to a stand-in for Cloud
// Asset Inventory that finds the resources of state (see assetSearch),
// for the rest of the test.
func useAssetSearch(t *testing.T, e googleEstate, state []*tfjson.StateResource) {
	t.Helper()
	t.Setenv(google.EndpointEnv, assetSearch(t, e.endpoint, state).URL)
}

// TestGoogleSelection checks discover google and an import through the
// selection file it writes. Discover lists every resource testdata/google
// creates into the file, and into the inventories it saves, and leaves out
// a firewall rule named like the rules of a project's default network
// (testdata/google-selection). With the file edited to exclude the Pub/Sub
// subscription and include that rule, import --selection imports what the
// file includes and nothing else, and every root plans as imports with no
// changes.
func TestGoogleSelection(t *testing.T) {
	e := seedGoogle(t)
	rules := seed(e.ctx, t, "google-selection", e.execPath, e.plugins)
	useAssetSearch(t, e, append(slices.Clone(e.state), rules...))

	listed := t.TempDir()
	f := checkDiscovered(t, discoverGoogle(t, e, listed), listed, "google", e.state)
	rule := selectionEntry(t, f, "google_compute_firewall", "/default-allow-e2e")
	if r := f.Resources[rule]; r.Include || !strings.Contains(r.Reason, "default network") {
		t.Errorf("the rule named like the default network's: include=%v %q, want it left out for its name", r.Include, r.Reason)
	}
	subscription := selectionEntry(t, f, "google_pubsub_subscription", "infraharvest-e2e-worker")
	f.Resources[rule].Include = true
	f.Resources[subscription].Include = false
	edited := filepath.Join(t.TempDir(), "selection.yaml")
	if err := f.Save(edited); err != nil {
		t.Fatal(err)
	}

	out := importGoogle(t, e, "", "--selection="+edited)
	coverage := coverageOf(t, out)
	checkCoverage(t, "--selection", coverage, nil)
	checkImportsSelected(t, f, coverage)
	if !excludedIn(coverage, "google_pubsub_subscription", f.Resources[subscription].ID) {
		t.Errorf("--selection: the subscription the selection file excludes isn't reported left out: %+v", coverage.Excluded)
	}
	for _, dir := range generatedDirs(t, out) {
		checkNoChanges(e.ctx, t, dir, e.execPath, e.plugins, nil)
	}
}

// TestGoogleResume checks an import with --resume into the output of a
// finished import of the same resources does nothing new: it generates no
// root again and leaves the configuration as it was.
func TestGoogleResume(t *testing.T) {
	e := seedGoogle(t)
	useAssetSearch(t, e, e.state)
	out := importGoogle(t, e, "", "--all")
	before, times := outputFiles(t, out), generatedTimes(t, out)
	logged := logDuring(func() { importGoogle(t, e, out, "--all", "--resume") })
	checkResumed(t, out, before, times, logged)
}

// TestGoogleReuseInventory checks an import with --reuse-inventory into
// the output discover listed into takes the resources from the
// inventories discover saved there, as its log says, and makes none of
// the listing requests discover made. It imports what the selection file
// includes, and every root plans as imports with no changes.
func TestGoogleReuseInventory(t *testing.T) {
	e := seedGoogle(t)
	search := assetSearch(t, e.endpoint, e.state)
	counter, listings := countListings(t, search.URL)
	t.Setenv(google.EndpointEnv, counter.URL)

	out := t.TempDir()
	selectionFile := discoverGoogle(t, e, out)
	if listings.Load() == 0 {
		t.Fatal("discover made no listing request the test counts: the counter misses them")
	}
	f, err := selection.Load(selectionFile)
	if err != nil {
		t.Fatal(err)
	}

	listings.Store(0)
	logged := logDuring(func() { importGoogle(t, e, out, "--selection="+selectionFile, "--reuse-inventory") })
	if n := listings.Load(); n != 0 {
		t.Errorf("--reuse-inventory made %d listing requests", n)
	}
	if !strings.Contains(logged, "google: using the resources discover listed") || strings.Contains(logged, "no saved inventory") || strings.Contains(logged, "listing again") {
		t.Errorf("--reuse-inventory didn't take the resources from the saved inventories; the log:\n%s", logged)
	}
	coverage := coverageOf(t, out)
	checkCoverage(t, "--reuse-inventory", coverage, nil)
	checkImportsSelected(t, f, coverage)
	for _, dir := range generatedDirs(t, out) {
		checkNoChanges(e.ctx, t, dir, e.execPath, e.plugins, nil)
	}
}

// countListings stands in front of the endpoint at target, which gets
// every request, and counts those that list resources: Cloud Asset
// Inventory's searches and the reads of the project's IAM policy.
func countListings(t *testing.T, target string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	listings := &atomic.Int64{}
	counter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ":searchAllResources") || strings.HasSuffix(r.URL.Path, ":getIamPolicy") {
			listings.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(counter.Close)
	return counter, listings
}

// TestGoogleIncremental checks --incremental adds a resource created
// after an import to the root that import generated: a Pub/Sub topic
// (testdata/google-incremental), after the blocks of its type's file
// in global's root or in a new one, with what the roots had as it was,
// and the roots plan as imports with no changes. Another import with
// --incremental adds nothing.
// Deleted out of band, the topic is reported no longer in the cloud, as
// Cloud Asset Inventory lists every topic of the project.
//
// It adds to the estate the other Google Cloud tests import, so CI runs
// it on a runner of its own.
func TestGoogleIncremental(t *testing.T) {
	e := seedGoogle(t)
	useAssetSearch(t, e, e.state)
	out := importGoogle(t, e, "", "--all")
	before := outputFiles(t, out)
	fixture := seedApplied(e.ctx, t, "google-incremental", e.execPath, e.plugins)
	added := fixture.resources(e.ctx, t)
	created := append(slices.Clone(e.state), added...)
	useAssetSearch(t, e, created)

	importGoogle(t, e, out, "--all", "--incremental")
	after := outputFiles(t, out)
	checkAddedTo(t, out, filepath.Join(out, "google", googleProject, "global"), "google_pubsub_topic", "infraharvest-e2e-added", before, after)
	coverage := coverageOf(t, out)
	checkCoverage(t, "--incremental", coverage, nil)
	if !slices.ContainsFunc(coverage.Excluded, func(x report.Excluded) bool { return x.Owner == report.OwnerTerraformRoot }) {
		t.Errorf("--incremental: nothing is reported as in a root already: %+v", coverage.Excluded)
	}
	imported := map[string]int{}
	for _, dir := range generatedDirs(t, out) {
		for typ, n := range checkNoChanges(e.ctx, t, dir, e.execPath, e.plugins, nil) {
			imported[typ] += n
		}
	}
	checkAllImported(t, "--incremental", imported, created)

	importGoogle(t, e, out, "--all", "--incremental")
	for name := range addedConfiguration(t, after, outputFiles(t, out)) {
		t.Errorf("--incremental with nothing new wrote to %s", name)
	}

	// The topic is deleted out of band, and the search no longer finds it.
	if err := fixture.tf.Destroy(e.ctx); err != nil {
		t.Fatalf("delete the added topic: %v", err)
	}
	useAssetSearch(t, e, e.state)
	importGoogle(t, e, out, "--all", "--incremental")
	coverage = coverageOf(t, out)
	checkCoverage(t, "--incremental after deleting the topic", coverage, nil)
	if len(coverage.NotInCloud) != 1 {
		t.Fatalf("--incremental after deleting the topic: no longer in the cloud %+v, want the topic", coverage.NotInCloud)
	}
	if gone := coverage.NotInCloud[0]; gone.Path != "google/"+googleProject+"/global" || !strings.HasPrefix(gone.Address, "google_pubsub_topic.") || !strings.HasSuffix(gone.ID, "infraharvest-e2e-added") || gone.Scope == "" {
		t.Errorf("no longer in the cloud: got %+v, want the deleted topic", gone)
	}
}
