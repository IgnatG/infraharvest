// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/engine"
)

// The staging directory is always its own directory under the output's
// checkpoints, as engine.Add empties it: a root outside the output
// (--path-pattern need not put it there) gets one named after a hash.
func TestStagingDirStaysUnderTheOutput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	base := filepath.Join(out, CheckpointDir, "incremental")

	if got, want := stagingDir(out, filepath.Join(out, "aws", "123")), filepath.Join(base, "aws", "123"); got != want {
		t.Errorf("a root in the output: got %s, want %s", got, want)
	}
	for _, dir := range []string{
		filepath.Join(filepath.Dir(out), "elsewhere", "aws"),
		filepath.Dir(out),
		out,
	} {
		got := stagingDir(out, dir)
		rel, err := filepath.Rel(base, got)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.Contains(rel, string(filepath.Separator)) || len(rel) != 16 {
			t.Errorf("%s: staging %s isn't a directory of its own under %s", dir, got, base)
		}
		if got != stagingDir(out, dir) {
			t.Errorf("%s: the staging directory changes between calls", dir)
		}
	}
}

// An error before the import knows which resources are new fails them all.
func TestAddToRootFailsEveryImportOnACheckpointError(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "aws", "123")
	path := checkpointPath(out, dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &engineRun{options: ImportOptions{PathOutput: out}}
	imports := []engine.Import{{Type: "aws_vpc", Name: "main", ID: "vpc-1"}, {Type: "aws_subnet", Name: "a", ID: "subnet-1"}}

	failed, result, holds, err := r.addToRoot(context.Background(), dir, imports, engine.Options{}, terraformRun{})

	if err == nil || result != nil || holds != nil {
		t.Fatalf("want the checkpoint error, got result=%v holds=%v err=%v", result, holds, err)
	}
	if !reflect.DeepEqual(failed, imports) {
		t.Errorf("failed: got %v, want every import", failed)
	}
}
