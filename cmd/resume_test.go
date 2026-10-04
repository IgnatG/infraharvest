// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/IgnatG/infraharvest/engine"
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
