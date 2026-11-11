// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file larger than the walk reads fails it, rather than being skipped by
// the checks that walk the output.
func TestWalkOutputRefusesALargeFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.tf"), []byte("# small\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen []string
	if err := walkOutputUpTo(dir, 16, func(rel, _ string) { seen = append(seen, rel) }); err != nil || len(seen) != 1 {
		t.Fatalf("got %v, %v; want small.tf read", seen, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "large.tf"), []byte(strings.Repeat("#", 17)), 0o600); err != nil {
		t.Fatal(err)
	}
	err := walkOutputUpTo(dir, 16, func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "large.tf") {
		t.Fatalf("got %v, want an error naming large.tf", err)
	}
}
