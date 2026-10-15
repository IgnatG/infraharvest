// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "selection.yaml")

	for _, content := range []string{"first\n", "second, longer\n", ""} {
		if err := WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != content {
			t.Errorf("content %q, want %q", got, content)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("files left in %s: %v", dir, entries)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf("mode %v, want 0644", info.Mode().Perm())
		}
	}
}

func TestWriteFileFailureKeepsTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.json")
	if err := os.WriteFile(path, []byte("whole"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory where the file should be can't be replaced.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(sub, []byte("x"), 0o644); err == nil {
		t.Error("want an error writing over a directory")
	}
	if got, _ := os.ReadFile(path); string(got) != "whole" {
		t.Errorf("content %q, want the file untouched", got)
	}
	// No temporary file left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("files left in %s: %v", dir, entries)
	}
}
