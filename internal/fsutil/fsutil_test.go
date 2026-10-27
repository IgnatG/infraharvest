// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteFile(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// A tree of directories and regular files passes, as does one that isn't
// there; a symbolic link anywhere in it fails, naming it, except under a
// directory it skips, which mustn't be a link itself.
func TestCheckTree(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	secret := filepath.Join(outside, "authorized_keys")
	if err := os.WriteFile(secret, []byte("ssh-ed25519 key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(t.TempDir(), "probe")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}

	for _, tc := range []struct {
		name string
		// files and dirs to make under the root, and links to make
		// there, to the target each names: "file" or "dir"
		files, dirs []string
		links       map[string]string
		// bad is the path, under the root, the error names; "" passes
		bad string
	}{
		{name: "not there"},
		{name: "plain", dirs: []string{"root/checkpoints/aws"}, files: []string{"root/lock", "root/checkpoints/aws/a.json"}},
		{name: "root a link", links: map[string]string{"root": "dir"}, bad: "root"},
		{name: "root a file", files: []string{"root"}, bad: "root"},
		{name: "a file a link", dirs: []string{"root"}, links: map[string]string{"root/lock": "file"}, bad: "root/lock"},
		{name: "a dangling link", dirs: []string{"root"}, links: map[string]string{"root/lock": "missing"}, bad: "root/lock"},
		{name: "a directory a link", dirs: []string{"root/drift"}, links: map[string]string{"root/drift/0a1b": "dir"}, bad: "root/drift/0a1b"},
		{name: "a link on the way", dirs: []string{"root/incremental"}, links: map[string]string{"root/incremental/aws": "dir"}, bad: "root/incremental/aws"},
		{name: "a link in a skipped directory", dirs: []string{"root/drift/0a1b/.terraform/providers"}, links: map[string]string{"root/drift/0a1b/.terraform/providers/aws": "dir"}},
		{name: "a skipped directory a link", dirs: []string{"root/drift/0a1b"}, links: map[string]string{"root/drift/0a1b/.terraform": "dir"}, bad: "root/drift/0a1b/.terraform"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := t.TempDir()
			for _, dir := range tc.dirs {
				if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, file := range tc.files {
				if err := os.WriteFile(filepath.Join(base, file), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for link, target := range tc.links {
				to := map[string]string{"file": secret, "dir": outside, "missing": filepath.Join(outside, "missing")}[target]
				if err := os.Symlink(to, filepath.Join(base, link)); err != nil {
					t.Fatal(err)
				}
			}
			err := CheckTree(filepath.Join(base, "root"), ".terraform")
			if tc.bad == "" {
				if err != nil {
					t.Errorf("got %v, want it to pass", err)
				}
				return
			}
			if want := filepath.Join(base, tc.bad) + " "; err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("got %v, want %s named", err, want)
			}
		})
	}
	if got, err := os.ReadFile(secret); err != nil || string(got) != "ssh-ed25519 key\n" {
		t.Errorf("the link's target changed: %q, %v", got, err)
	}
}

// A regular file passes, as does one that isn't there; a symbolic link or
// a directory fails.
func TestCheckFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "lock")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(dir, "missing")} {
		if err := CheckFile(path); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
	if err := CheckFile(dir); err == nil || !strings.Contains(err.Error(), "isn't a regular file") {
		t.Errorf("a directory: got %v, want it refused", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if err := CheckFile(link); err == nil || !strings.Contains(err.Error(), "is a symbolic link") {
		t.Errorf("a link: got %v, want it refused", err)
	}
}
