// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// An edit that fails after changing a file leaves nothing of it behind.
func TestVerifyRestoresWhenTheEditFails(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, networkGenerated)
	tf := &fakeTerraform{dir: dir}
	failure := errors.New("halfway")
	edit := func() (bool, error) {
		if err := os.WriteFile(filepath.Join(dir, GeneratedFileName), []byte("# changed\n"), 0o644); err != nil {
			return false, err
		}
		if err := os.WriteFile(filepath.Join(dir, LocalsFileName), []byte("locals {}\n"), 0o644); err != nil {
			return false, err
		}
		return true, failure
	}

	kept, err := verify(context.Background(), tf, dir, changeSummary{}, nil, edit, GeneratedFileName, LocalsFileName)
	if kept || !errors.Is(err, failure) {
		t.Fatalf("got kept=%v err=%v, want the edit's error", kept, err)
	}

	if got := readFile(t, dir, GeneratedFileName); got != networkGenerated {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, LocalsFileName)); err == nil {
		t.Error("locals.tf not removed")
	}
	if len(tf.calls) != 0 {
		t.Errorf("planned after a failed edit: %v", tf.calls)
	}
}
