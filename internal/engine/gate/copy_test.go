// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package gate

import (
	"os"
	"path/filepath"
	"testing"
)

// The copy verify checks leaves out checkov's configuration files, at any
// depth, which would let a root set the options checkov scans it with,
// and keeps the root's other files.
func TestVerifyCopyLeavesOutCheckovConfig(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "root")
	files := []string{"main.tf", ".checkov.yaml", ".checkov.yml", "vars/.checkov.yaml", "vars/prod.tfvars"}
	for _, name := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x = 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	copied, remove, err := VerifyCopy(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	for _, name := range files {
		_, err := os.Stat(filepath.Join(copied, filepath.FromSlash(name)))
		if want := !checkovConfig(filepath.Base(name)); (err == nil) != want {
			t.Errorf("%s copied: %t, want %t", name, err == nil, want)
		}
	}
}
