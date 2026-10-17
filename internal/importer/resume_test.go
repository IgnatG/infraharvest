// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
)

func TestCheckpoints(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "111122223333", "eu-west-2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	imports := []engine.Import{{Type: "aws_vpc", Name: "main", ID: "vpc-0abc1234"}}
	opts := engine.Options{Config: map[string][]byte{engine.VersionsFileName: []byte("terraform {}\n")}}
	fp, err := fingerprint("1.16.5", imports, opts)
	if err != nil {
		t.Fatal(err)
	}
	result := &engine.Result{Imported: imports, Gate: engine.Gate{{Name: engine.CheckPlan, Passed: true}}}

	// Nothing recorded yet.
	if got, err := resumed(out, dir, fp); err != nil || got != nil {
		t.Fatalf("before a checkpoint: %v, %v", got, err)
	}
	if err := saveCheckpoint(out, dir, fp, result); err != nil {
		t.Fatal(err)
	}
	// The checkpoint counts only while the root's files are there.
	if got, err := resumed(out, dir, fp); err != nil || got != nil {
		t.Fatalf("without the root's files: %v, %v", got, err)
	}
	for _, name := range []string{engine.GeneratedFileName, engine.ImportsFileName, engine.VersionsFileName} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := resumed(out, dir, fp)
	if err != nil || !reflect.DeepEqual(got, result) {
		t.Errorf("resumed: got %+v, %v, want %+v", got, err, result)
	}

	// Other imports or options make another root.
	other, err := fingerprint("1.16.5", append(imports, engine.Import{Type: "aws_subnet", Name: "a", ID: "subnet-1"}), opts)
	if err != nil {
		t.Fatal(err)
	}
	newerEngine, err := fingerprint("1.17.0", imports, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{other, newerEngine} {
		if f == fp {
			t.Error("fingerprint unchanged")
		}
		if got, err := resumed(out, dir, f); err != nil || got != nil {
			t.Errorf("resumed with another fingerprint: %v, %v", got, err)
		}
	}
}

// Listing order varies between runs; the fingerprint doesn't.
func TestFingerprintIgnoresOrder(t *testing.T) {
	a := []engine.Import{{Type: "aws_vpc", Name: "a", ID: "vpc-1"}, {Type: "aws_subnet", Name: "b", ID: "subnet-1"}}
	b := []engine.Import{a[1], a[0]}
	fa, err := fingerprint("1.16.5", a, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fb, err := fingerprint("1.16.5", b, engine.Options{})
	if err != nil || fa != fb {
		t.Errorf("fingerprints differ: %s, %s, %v", fa, fb, err)
	}
	if a[0].Type != "aws_vpc" {
		t.Error("fingerprint reordered the caller's imports")
	}
}

func TestClearGenerated(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "123", "eu-west-2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// generated_2.tf is what --incremental added to the root; network.tf
	// what the checkpoint says the configuration was split into.
	for _, name := range []string{engine.GeneratedFileName, engine.ImportsFileName, engine.VersionsFileName, engine.AddedFileName(2), "network.tf", "notes.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveCheckpoint(out, dir, "fp", &engine.Result{Files: []string{"network.tf", "../outside.tf"}}); err != nil {
		t.Fatal(err)
	}
	if err := clearGenerated(out, dir); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{engine.GeneratedFileName: false, engine.ImportsFileName: false, engine.AddedFileName(2): false, "network.tf": false, engine.VersionsFileName: true, "notes.md": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", name, err == nil, want)
		}
	}
}

// A root is only generated into a directory an import wrote, or one
// without .tf files; --resume clears only what an import wrote.
func TestClaimRoot(t *testing.T) {
	out := t.TempDir()
	write := func(dir string, names ...string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}

	// An unrelated root keeps its files, with or without --resume.
	unrelated := filepath.Join(out, "aws", "111", "eu-west-2")
	write(unrelated, "main.tf", engine.VariablesFileName, engine.ReadmeFileName)
	for _, resume := range []bool{false, true} {
		err := claimRoot(out, unrelated, "fp", resume)
		if err == nil || !strings.Contains(err.Error(), unrelated) || !strings.Contains(err.Error(), "main.tf") {
			t.Errorf("resume=%v: want an error naming the directory and main.tf, got %v", resume, err)
		}
	}
	for _, name := range []string{"main.tf", engine.VariablesFileName, engine.ReadmeFileName} {
		if !exists(filepath.Join(unrelated, name)) {
			t.Errorf("%s was removed from the unrelated root", name)
		}
	}
	if exists(checkpointPath(out, unrelated)) {
		t.Error("the unrelated root got a checkpoint")
	}

	// A new root is claimed: its checkpoint records that it started, which
	// --resume doesn't take as a generated root.
	fresh := filepath.Join(out, "aws", "222", "eu-west-2")
	if err := claimRoot(out, fresh, "fp", false); err != nil {
		t.Fatal(err)
	}
	if !exists(checkpointPath(out, fresh)) {
		t.Fatal("no checkpoint for the new root")
	}
	write(fresh, engine.ImportsFileName, engine.VersionsFileName, engine.GeneratedFileName)
	if got, err := resumed(out, fresh, "fp"); err != nil || got != nil {
		t.Errorf("resumed a root that didn't finish: %v, %v", got, err)
	}

	// --resume clears what the import wrote there, and only that.
	write(fresh, engine.VariablesFileName, engine.ReadmeFileName, "notes.md")
	if err := claimRoot(out, fresh, "fp", true); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{engine.ImportsFileName: false, engine.GeneratedFileName: false, engine.VariablesFileName: false, engine.ReadmeFileName: false, "notes.md": true} {
		if got := exists(filepath.Join(fresh, name)); got != want {
			t.Errorf("after --resume, %s: exists=%v, want %v", name, got, want)
		}
	}

	// Without --resume, an earlier run's checkpoint stays as it is.
	done := &engine.Result{Files: []string{"network.tf"}}
	if err := saveCheckpoint(out, fresh, "earlier", done); err != nil {
		t.Fatal(err)
	}
	if err := claimRoot(out, fresh, "fp", false); err != nil {
		t.Fatal(err)
	}
	write(fresh, engine.ImportsFileName, engine.VersionsFileName, "network.tf")
	if got, err := resumed(out, fresh, "earlier"); err != nil || !reflect.DeepEqual(got, done) {
		t.Errorf("the earlier checkpoint changed: %+v, %v", got, err)
	}
}
