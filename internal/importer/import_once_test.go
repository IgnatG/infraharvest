// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestImportOnceIsOnByDefault(t *testing.T) {
	for _, on := range []string{"", "1", "true", " yes ", "anything"} {
		t.Setenv(importOnceEnv, on)
		if importOnceFor(terraformRun{}) == nil {
			t.Errorf("%q switched import-once state off", on)
		}
	}
	for _, off := range []string{"0", "false", "FALSE", " no ", "off"} {
		t.Setenv(importOnceEnv, off)
		if importOnceFor(terraformRun{}) != nil {
			t.Errorf("%q didn't switch import-once state off", off)
		}
	}
}

// A root that fails its plan check after planning against a snapshot is
// generated again once without it, and succeeds.
func TestRootIsGeneratedAgainWithoutTheSnapshot(t *testing.T) {
	t.Setenv(importOnceEnv, "1")
	out := filepath.Join(t.TempDir(), "out")
	f := &fakeEngine{snapshotFails: "alpha"}
	run := importPath(t, pathOptions(out, f, "alpha", "beta"))

	if want := []string{"alpha", "alpha", "beta"}; !slices.Equal(f.generated, want) {
		t.Errorf("generated %v, want %v", f.generated, want)
	}
	if want := []bool{true, false, true}; !slices.Equal(f.withSnapshot, want) {
		t.Errorf("with the snapshot: %v, want %v", f.withSnapshot, want)
	}
	if len(run.failures) != 0 {
		t.Errorf("failures: %v", run.failures)
	}
	for _, d := range run.report.Directories {
		if d.Error != "" {
			t.Errorf("%s: %s", d.Path, d.Error)
		}
	}
}
