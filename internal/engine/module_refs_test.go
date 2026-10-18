// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// ReleaseModules removes the modules a root took up that this process
// wrote and no other root calls, such as when the root is put back.
func TestReleaseModules(t *testing.T) {
	modules := t.TempDir()
	root, other := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	write := func(at string) error { return os.WriteFile(filepath.Join(at, "main.tf"), nil, 0o644) }
	for _, hold := range []struct{ root, module string }{{root, "only"}, {root, "shared"}, {other, "shared"}} {
		if _, err := holdModule(hold.root, filepath.Join(modules, hold.module), write); err != nil {
			t.Fatal(err)
		}
	}
	if err := ReleaseModules(root); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(modules, "only")) || !exists(filepath.Join(modules, "shared")) {
		t.Errorf("want only the module no other root calls removed")
	}
	if err := ReleaseModules(other); err != nil || exists(filepath.Join(modules, "shared")) {
		t.Errorf("want the shared module removed once its last root lets go: %v", err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
