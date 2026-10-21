// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || azure)

package e2e

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// azureRoot is the root the Azure tests' imports generate, for the
// subscription and the resource group testdata/azure creates.
func azureRoot(out string) string {
	return filepath.Join(out, "azure", os.Getenv("ARM_SUBSCRIPTION_ID"), azureResourceGroup)
}

// TestAzureSelection checks discover azure and an import through the
// selection file it writes. Discover lists every resource testdata/azure
// creates into the file, and into the inventory it saves, and leaves out
// a network security group whose tag says Pulumi manages it
// (testdata/azure-selection). With the file edited to exclude the public
// IP and include that group, import --selection imports what the file
// includes and nothing else, and every root plans as imports with no
// changes.
func TestAzureSelection(t *testing.T) {
	e := seedAzure(t)
	seed(e.ctx, t, "azure-selection", e.execPath, e.plugins)

	listed := t.TempDir()
	f := checkDiscovered(t, discoverAzure(t, e, listed), listed, "azurerm", e.state)
	pulumi := selectionEntry(t, f, "azurerm_network_security_group", "/infraharvest-e2e-pulumi")
	if r := f.Resources[pulumi]; r.Include || !strings.Contains(r.Reason, "Pulumi") {
		t.Errorf("the group Pulumi manages: include=%v %q, want it left out for its tag", r.Include, r.Reason)
	}
	ip := selectionEntry(t, f, "azurerm_public_ip", "/infraharvest-e2e-app")
	f.Resources[pulumi].Include = true
	f.Resources[ip].Include = false
	edited := filepath.Join(t.TempDir(), "selection.yaml")
	if err := f.Save(edited); err != nil {
		t.Fatal(err)
	}

	out := importAzure(t, e, "", "--selection="+edited)
	coverage := coverageOf(t, out)
	checkCoverage(t, "--selection", coverage, azureEmulatorFailure)
	checkImportsSelected(t, f, coverage)
	if !excludedIn(coverage, "azurerm_public_ip", f.Resources[ip].ID) {
		t.Errorf("--selection: the public IP the selection file excludes isn't reported left out: %+v", coverage.Excluded)
	}
	for _, dir := range generatedDirs(t, out) {
		checkNoChanges(e.ctx, t, dir, e.execPath, e.plugins, nil)
	}
}

// TestAzureResume checks an import with --resume into the output of a
// finished import of the same resources does nothing new: it generates no
// root again and leaves the configuration as it was.
func TestAzureResume(t *testing.T) {
	e := seedAzure(t)
	out := importAzure(t, e, "", "--all")
	before, times := outputFiles(t, out), generatedTimes(t, out)
	logged := logDuring(func() { importAzure(t, e, out, "--all", "--resume") })
	checkResumed(t, out, before, times, logged)
}

// TestAzureReuseInventory checks an import with --reuse-inventory into the
// output discover listed into takes the resources from the inventory
// discover saved there, as its log says, instead of listing them again.
// It imports what the selection file includes, and every root plans as
// imports with no changes. floci-az is reached over HTTPS straight from
// the listers, so the test can't count their requests as the Google Cloud
// one does.
func TestAzureReuseInventory(t *testing.T) {
	e := seedAzure(t)
	out := t.TempDir()
	selectionFile := discoverAzure(t, e, out)
	f, err := selection.Load(selectionFile)
	if err != nil {
		t.Fatal(err)
	}

	logged := logDuring(func() { importAzure(t, e, out, "--selection="+selectionFile, "--reuse-inventory") })
	if !strings.Contains(logged, "azurerm: using the resources discover listed") || strings.Contains(logged, "no saved inventory") || strings.Contains(logged, "listing again") {
		t.Errorf("--reuse-inventory didn't take the resources from the saved inventory; the log:\n%s", logged)
	}
	coverage := coverageOf(t, out)
	checkCoverage(t, "--reuse-inventory", coverage, azureEmulatorFailure)
	checkImportsSelected(t, f, coverage)
	for _, dir := range generatedDirs(t, out) {
		checkNoChanges(e.ctx, t, dir, e.execPath, e.plugins, nil)
	}
}

// TestAzureIncremental checks --incremental adds a resource created after
// an import to the root that import generated: a network security group
// (testdata/azure-incremental), after the blocks of its type's file or in
// a new one, with what the root had as it was, and the root plans as
// imports with no changes. Another import with --incremental adds
// nothing.
//
// It adds to the estate the other Azure tests import, so CI runs it on a
// runner of its own.
func TestAzureIncremental(t *testing.T) {
	e := seedAzure(t)
	out := importAzure(t, e, "", "--all")
	before := outputFiles(t, out)
	added := seed(e.ctx, t, "azure-incremental", e.execPath, e.plugins)

	importAzure(t, e, out, "--all", "--incremental")
	after := outputFiles(t, out)
	checkAddedTo(t, out, azureRoot(out), "azurerm_network_security_group", "infraharvest-e2e-added", before, after)
	coverage := coverageOf(t, out)
	checkCoverage(t, "--incremental", coverage, azureEmulatorFailure)
	if !slices.ContainsFunc(coverage.Excluded, func(x report.Excluded) bool { return x.Reason == importer.InRootReason }) {
		t.Errorf("--incremental: nothing is reported %s: %+v", importer.InRootReason, coverage.Excluded)
	}
	imported := map[string]int{}
	for _, dir := range generatedDirs(t, out) {
		for typ, n := range checkNoChanges(e.ctx, t, dir, e.execPath, e.plugins, nil) {
			imported[typ] += n
		}
	}
	checkAllImported(t, "--incremental", imported, append(slices.Clone(e.state), added...))

	importAzure(t, e, out, "--all", "--incremental")
	for name := range addedConfiguration(t, after, outputFiles(t, out)) {
		t.Errorf("--incremental with nothing new wrote to %s", name)
	}
}
