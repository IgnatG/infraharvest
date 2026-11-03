// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// A write back that fails partway restores the files it already wrote.
func TestWriteBackRestoresOnFailure(t *testing.T) {
	out, copyDir := t.TempDir(), t.TempDir()
	c := &confirmedRoots{out: out, copy: copyDir, hashes: map[string][sha256.Size]byte{}}
	for _, name := range []string{"a/main.tf", "b/main.tf"} {
		original := []byte("# original " + name + "\n")
		for _, dir := range []string{out, copyDir} {
			if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(out, name), original, 0o600); err != nil {
			t.Fatal(err)
		}
		c.hashes[filepath.FromSlash(name)] = sha256.Sum256(original)
		if err := os.WriteFile(filepath.Join(copyDir, name), []byte("# accepted "+name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	written := 0
	writeBackFile = func(path string, data []byte, perm fs.FileMode) error {
		if written++; written == 2 {
			return errors.New("disk full")
		}
		return os.WriteFile(path, data, perm)
	}
	t.Cleanup(func() { writeBackFile = defaultWriteBackFile })

	if err := c.writeBack(); err == nil {
		t.Fatal("want an error")
	}
	for _, name := range []string{"a/main.tf", "b/main.tf"} {
		got, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		if want := "# original " + name + "\n"; string(got) != want {
			t.Errorf("%s = %q, want it restored to %q", name, got, want)
		}
	}
}
