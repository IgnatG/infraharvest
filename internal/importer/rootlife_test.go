// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
)

// mergedRoot generates the root of services in out/fake/all, with a
// resource block for each, then sets it aside as its stack's merge does
// (see setAside), and returns its directory.
func mergedRoot(t *testing.T, out string, services ...string) string {
	t.Helper()
	options := pathOptions(out, &fakeEngine{}, services...)
	options.Layout = "{out}/{provider}/all/"
	importPath(t, options)
	dir := filepath.Join(out, "fake", "all")
	var blocks []string
	for _, service := range services {
		blocks = append(blocks, "resource \"fake_thing\" \""+service+"\" {}\n")
	}
	if err := os.WriteFile(filepath.Join(dir, engine.GeneratedFileName), []byte(strings.Join(blocks, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setAside(out, dir); err != nil {
		t.Fatal(err)
	}
	if set, err := premerged(out, dir); err != nil || !set {
		t.Fatalf("set aside: %v, %v", set, err)
	}
	return dir
}

// A root its stack's merge set aside, which may be in use through the
// merged root, is refused by a plain import, and stays set aside.
func TestListCallRefusesAMergedRoot(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	dir := mergedRoot(t, out, "alpha")

	f := &fakeEngine{}
	options := pathOptions(out, f, "alpha")
	options.Layout = "{out}/{provider}/all/"
	run := importPath(t, options)
	if len(f.generated) != 0 {
		t.Errorf("generated %v", f.generated)
	}
	if len(run.failures) != 1 || !strings.Contains(run.failures[0].Error(), "was merged into the root its stack's environments share") {
		t.Errorf("failures %v, want the merged root refused", run.failures)
	}
	if set, err := premerged(out, dir); err != nil || !set {
		t.Errorf("the root isn't set aside any more: %v, %v", set, err)
	}
}

// --incremental brings a root its stack's merge set aside back, adds what
// is new to it, and sets it aside again, for the merge phase to merge its
// stack again.
func TestListCallAddsToAMergedRoot(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	dir := mergedRoot(t, out, "alpha")

	f := &fakeEngine{}
	options := pathOptions(out, f, "alpha", "beta")
	options.Layout = "{out}/{provider}/all/"
	options.Incremental = true
	run := importPath(t, options)
	if len(f.generated) != 0 || len(run.failures) != 0 {
		t.Fatalf("generated %v, failures %v, want the root added to", f.generated, run.failures)
	}
	if added := f.added["all"]; len(added) != 1 || added[0].ID != "id-beta" {
		t.Errorf("added %+v, want beta only", added)
	}
	if !slices.Equal(f.readmes, []string{"all"}) {
		t.Errorf("README written for %v, want the root added to", f.readmes)
	}
	if set, err := premerged(out, dir); err != nil || !set {
		t.Errorf("the root isn't set aside again: %v, %v", set, err)
	}
	if _, err := os.Stat(filepath.Join(premergeDir(out, dir), engine.AddedFileName(2))); err != nil {
		t.Errorf("what was added isn't set aside with the root: %v", err)
	}
	result, err := checkpointResult(out, dir)
	if err != nil || result == nil {
		t.Fatalf("checkpoint: %+v, %v", result, err)
	}
	var ids []string
	for _, imp := range result.Imported {
		ids = append(ids, imp.ID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"id-alpha", "id-beta"}) {
		t.Errorf("the checkpoint holds %v, want alpha and beta", ids)
	}
	if got := directoryPaths(run); !slices.Equal(got, []string{"fake/all"}) {
		t.Errorf("reported directories %v", got)
	}
}

// --regenerate generates a root its stack's merge set aside again, with
// no moved block following what it had: its environment applies the
// merged root.
func TestListCallRegeneratesAMergedRoot(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	mergedRoot(t, out, "alpha")

	f := &fakeEngine{}
	options := pathOptions(out, f, "alpha")
	options.Layout = "{out}/{provider}/all/"
	options.Regenerate = true
	run := importPath(t, options)
	if !slices.Equal(f.generated, []string{"all"}) || len(run.failures) != 0 {
		t.Fatalf("generated %v, failures %v, want the root generated again", f.generated, run.failures)
	}
	if previous := f.opts["all"].Previous; previous != nil {
		t.Errorf("generated again from %+v, want nothing to move from", previous)
	}
}

// decideRoot decides once what a run does with each root, by its mode and
// what earlier imports left.
func TestDecideRoot(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	importPath(t, pathOptions(out, &fakeEngine{}, "alpha"))
	alpha := filepath.Join(out, "fake", "alpha")
	if err := os.WriteFile(filepath.Join(alpha, engine.GeneratedFileName), []byte("resource \"fake_thing\" \"alpha\" {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(out, "fake", "foreign")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "main.tf"), []byte("# someone's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp := func(dir string) string {
		c, err := readCheckpoint(out, dir)
		if err != nil || c == nil {
			return "none"
		}
		return c.Fingerprint
	}
	for _, tc := range []struct {
		name   string
		dir    string
		set    func(*Options)
		want   rootMode
		reason string
	}{
		{"new", filepath.Join(out, "fake", "new"), func(*Options) {}, rootFresh, ""},
		{"foreign", foreign, func(*Options) {}, rootRefused, "no import by infraharvest wrote"},
		{"in use", alpha, func(*Options) {}, rootRefused, "--regenerate"},
		{"regenerate", alpha, func(o *Options) { o.Regenerate = true }, rootRegenerated, ""},
		{"resume", alpha, func(o *Options) { o.Resume = true }, rootResumed, ""},
		{"incremental", alpha, func(o *Options) { o.Incremental = true }, rootIncremental, ""},
	} {
		c := &listedCall{options: Options{Out: out}}
		tc.set(&c.options)
		d, err := c.decideRoot(tc.dir, fp(tc.dir))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if d.mode != tc.want || (tc.reason == "") != (d.reason == nil) || (d.reason != nil && !strings.Contains(d.reason.Error(), tc.reason)) {
			t.Errorf("%s: mode %d, reason %v; want mode %d, reason %q", tc.name, d.mode, d.reason, tc.want, tc.reason)
		}
	}
}
