// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"log"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// The checks of the Azure and Google Cloud tests of discover, --selection,
// --incremental, --resume and --reuse-inventory, which import from an
// emulator as the AWS tests do.

// logBuffer collects what the log says, from the import's goroutines too.
type logBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// logDuring runs fn and returns what the log said meanwhile, which still
// goes where it went.
func logDuring(fn func()) string {
	logged := &logBuffer{}
	writer := log.Writer()
	log.SetOutput(io.MultiWriter(writer, logged))
	defer log.SetOutput(writer)
	fn()
	logged.mu.Lock()
	defer logged.mu.Unlock()
	return logged.b.String()
}

// coverageOf returns the report of the import into out.
func coverageOf(t *testing.T, out string) report.Report {
	t.Helper()
	var coverage report.Report
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(out, report.Dir, "coverage.json"))), &coverage); err != nil {
		t.Fatal(err)
	}
	return coverage
}

// checkCoverage fails the test for each resource the import what reports
// left out or failed, for each check of a root that failed, and for each
// failure but those emulatorFailure, if not nil, accepts.
func checkCoverage(t *testing.T, what string, coverage report.Report, emulatorFailure func(string) bool) {
	t.Helper()
	failures := coverage.Failures
	if emulatorFailure != nil {
		failures = slices.DeleteFunc(slices.Clone(failures), emulatorFailure)
	}
	if coverage.Totals.LeftOut != 0 || coverage.Totals.Failed != 0 || len(failures) != 0 {
		t.Errorf("%s: coverage.json: %+v, failures %v", what, coverage.Totals, failures)
	}
	for _, d := range coverage.Directories {
		for _, l := range d.LeftOut {
			t.Errorf("%s: %s: %s (%s) left out: %v", what, d.Path, l.Address, l.ID, l.Errors)
		}
		for _, c := range d.Checks {
			if !c.Passed {
				t.Errorf("%s: %s: %s failed: %v", what, d.Path, c.Name, c.Details)
			}
		}
	}
}

// inventoryRecord is a resource in an inventory discover saved, as far as
// the tests read it.
type inventoryRecord struct {
	Service string `json:"service"`
	Type    string `json:"type"`
	ID      string `json:"id"`
}

// inventoryRecords returns the resources of the inventories discover saved
// for provider in out's checkpoints, one for each call it made.
func inventoryRecords(t *testing.T, out, provider string) []inventoryRecord {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(out, importer.CheckpointDir, "inventory", provider+"-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatalf("discover saved no inventory of %s under %s", provider, out)
	}
	var records []inventoryRecord
	for _, path := range paths {
		var inv struct {
			Provider string            `json:"provider"`
			Records  []inventoryRecord `json:"records"`
		}
		if err := json.Unmarshal([]byte(readFile(t, path)), &inv); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if inv.Provider != provider {
			t.Errorf("%s: an inventory of %q, want %q", path, inv.Provider, provider)
		}
		records = append(records, inv.Records...)
	}
	return records
}

// checkDiscovered checks the selection file and the inventories discover
// wrote into out: the file includes, of each type, a resource for each
// one the test created, and the inventories hold, of each type, as many
// resources as the file lists or more. It returns the file.
func checkDiscovered(t *testing.T, selectionFile, out, provider string, created []*tfjson.StateResource) *selection.File {
	t.Helper()
	f, err := selection.Load(selectionFile)
	if err != nil {
		t.Fatal(err)
	}
	included := map[string]int{}
	listed := map[string]int{}
	for _, r := range f.Resources {
		listed[r.Type]++
		if r.Include {
			included[r.Type]++
		} else {
			t.Logf("excluded by default: %s %s (%s)", r.Type, r.ID, r.Reason)
		}
	}
	seeded := map[string]int{}
	for _, r := range created {
		seeded[r.Type]++
	}
	for _, typ := range sortedKeys(seeded) {
		if included[typ] < seeded[typ] {
			t.Errorf("discover: %s: created %d, the selection file includes %d", typ, seeded[typ], included[typ])
		}
	}
	// The inventories hold what was listed, including what can't be
	// imported, which the selection file leaves out.
	inventoried := map[string]int{}
	for _, r := range inventoryRecords(t, out, provider) {
		inventoried[r.Type]++
	}
	for _, typ := range sortedKeys(listed) {
		if inventoried[typ] < listed[typ] {
			t.Errorf("discover: %s: the selection file lists %d, the inventories hold %d", typ, listed[typ], inventoried[typ])
		}
	}
	return f
}

// selectionEntry returns the index of the resource of resourceType f
// lists whose ID ends with suffix, in any case, as Azure compares IDs,
// failing the test if it lists none.
func selectionEntry(t *testing.T, f *selection.File, resourceType, suffix string) int {
	t.Helper()
	for i, r := range f.Resources {
		if r.Type == resourceType && strings.HasSuffix(strings.ToLower(r.ID), strings.ToLower(suffix)) {
			return i
		}
	}
	t.Fatalf("the selection file lists no %s whose ID ends with %s", resourceType, suffix)
	return -1
}

// checkImportsSelected checks the import that coverage reports imported
// the resources f includes, and only those.
func checkImportsSelected(t *testing.T, f *selection.File, coverage report.Report) {
	t.Helper()
	want := map[string]bool{}
	for _, r := range f.Resources {
		if r.Include {
			want[r.Type+" "+r.ID] = true
		}
	}
	got := map[string]bool{}
	for _, d := range coverage.Directories {
		for _, r := range d.Imported {
			typ, _, _ := strings.Cut(r.Address, ".")
			got[typ+" "+r.ID] = true
		}
	}
	for _, key := range slices.Sorted(maps.Keys(want)) {
		if !got[key] {
			t.Errorf("--selection: %s is included in the selection file but not imported", key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(got)) {
		if !want[key] {
			t.Errorf("--selection: %s is imported but not included in the selection file", key)
		}
	}
}

// excludedIn reports whether coverage leaves out the resource of
// resourceType with id.
func excludedIn(coverage report.Report, resourceType, id string) bool {
	return slices.ContainsFunc(coverage.Excluded, func(e report.Excluded) bool { return e.Type == resourceType && e.ID == id })
}

// checkResumed checks an import with --resume, whose log is logged, into
// out, which held before, generated no root again: the roots are as they
// were, and the log says each is unchanged.
func checkResumed(t *testing.T, out string, before map[string]string, times map[string]int64, logged string) {
	t.Helper()
	if after := generatedTimes(t, out); !reflect.DeepEqual(times, after) {
		t.Errorf("--resume generated roots again: %v, then %v", times, after)
	}
	after := outputFiles(t, out)
	for name, content := range before {
		if strings.HasSuffix(name, ".tf") && after[name] != content {
			t.Errorf("--resume changed %s", name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok && strings.HasSuffix(name, ".tf") {
			t.Errorf("--resume wrote %s", name)
		}
	}
	if n := strings.Count(logged, " is unchanged since it was generated (--resume)"); n != len(times) {
		t.Errorf("the log of --resume says %d roots are unchanged, want %d:\n%s", n, len(times), logged)
	}
}

// checkAddedTo checks what an import with --incremental added to out, from
// before to after: configuration for a resource of resourceType called
// name, in one file of the root in dir, and nothing in the other roots.
// The roots plan with no changes, which the caller checks.
func checkAddedTo(t *testing.T, out, dir, resourceType, name string, before, after map[string]string) {
	t.Helper()
	rel, err := filepath.Rel(out, dir)
	if err != nil {
		t.Fatal(err)
	}
	rel = filepath.ToSlash(rel) + "/"
	added := addedConfiguration(t, before, after)
	if len(added) != 1 {
		t.Fatalf("--incremental added to %v, want to one file of %s", slices.Sorted(maps.Keys(added)), rel)
	}
	for file, content := range added {
		if !strings.HasPrefix(file, rel) || strings.Contains(strings.TrimPrefix(file, rel), "/") {
			t.Errorf("--incremental added to %s, want to the root %s", file, rel)
		}
		if !strings.Contains(content, `resource "`+resourceType+`"`) || !strings.Contains(content, `"`+name+`"`) {
			t.Errorf("--incremental added to %s, without the %s %s:\n%s", file, resourceType, name, content)
		}
	}
	// The other roots stay as they were, to the byte.
	for file, content := range before {
		if !strings.HasPrefix(file, rel) && strings.HasSuffix(file, ".tf") && after[file] != content {
			t.Errorf("--incremental changed %s, in another root", file)
		}
	}
}

// checkAllImported checks the plans of an import what imported, by type,
// hold every resource of created.
func checkAllImported(t *testing.T, what string, imported map[string]int, created []*tfjson.StateResource) {
	t.Helper()
	seeded := map[string]int{}
	for _, r := range created {
		seeded[r.Type]++
	}
	for _, typ := range sortedKeys(seeded) {
		if imported[typ] < seeded[typ] {
			t.Errorf("%s: %s: created %d, imported %d", what, typ, seeded[typ], imported[typ])
		}
	}
}
