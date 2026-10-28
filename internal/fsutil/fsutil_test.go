// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
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

// WriteFile gives the file the mode asked for, a private one included,
// and fails, leaving nothing behind, where it can't make its temporary
// file: in a directory that isn't there, or that it can't write to.
func TestWriteFileModeAndPlace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if runtime.GOOS != "windows" {
		private := filepath.Join(dir, "token")
		if err := WriteFile(private, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(private); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("mode %v (%v), want 0600", info.Mode().Perm(), err)
		}
	}
	if err := WriteFile(filepath.Join(dir, "missing", "file"), []byte("x"), 0o644); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("in a missing directory: got %v, want it not to exist", err)
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return
	}
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if err := WriteFile(filepath.Join(locked, "file"), []byte("x"), 0o644); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("in a read-only directory: got %v, want a permission error", err)
	}
	if entries, err := os.ReadDir(locked); err != nil || len(entries) != 0 {
		t.Errorf("left in the read-only directory: %v, %v", entries, err)
	}
}

// Writers of one file at the same time, as accounts imported in parallel
// write the output's .gitignore, leave it whole, as one of them wrote it,
// and no temporary file behind: run with -race.
func TestWriteFileConcurrently(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	contents := make([]string, 16)
	for i := range contents {
		contents[i] = strings.Repeat(string(rune('a'+i)), 4096+i)
	}
	var wg sync.WaitGroup
	for _, content := range contents {
		wg.Go(func() {
			if err := WriteFile(path, []byte(content), 0o644); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(contents, string(got)) {
		t.Errorf("the file is no writer's whole content: %d bytes", len(got))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("files left in %s: %v", dir, entries)
	}
}

// A path CheckTree or CheckFile can't look at fails, rather than passing
// as one that isn't there: one under a file, and, for CheckTree, a
// directory under the root it can't read.
func TestCheckUnreadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	under := filepath.Join(file, "sub")
	// Windows answers a path under a file as one that isn't there.
	if runtime.GOOS != "windows" {
		if err := CheckTree(under); err == nil {
			t.Error("CheckTree under a file: want an error")
		}
		if err := CheckFile(under); err == nil {
			t.Error("CheckFile under a file: want an error")
		}
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return
	}
	root := filepath.Join(dir, "root")
	sealed := filepath.Join(root, "sealed")
	if err := os.MkdirAll(sealed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sealed, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o755) })
	if err := CheckTree(root); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("CheckTree with an unreadable directory: got %v, want a permission error", err)
	}
	// Unless it is one CheckTree skips.
	if err := CheckTree(root, "sealed"); err != nil {
		t.Errorf("CheckTree skipping it: %v", err)
	}
}

// A tree of directories, regular files and links to regular files under
// it passes, as does one that isn't there, and a root that is itself a
// link; a link to a directory, out of the root or to nothing fails,
// naming it under the root as given, except under a directory it skips.
func TestCheckContained(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	secret := filepath.Join(outside, "id_ed25519")
	if err := os.WriteFile(secret, []byte("private key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(t.TempDir(), "probe")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}

	for _, tc := range []struct {
		name string
		// files and dirs to make under the base, and links to make there,
		// to the target each names: "file" or "dir" outside, "missing",
		// or a path under the base
		files, dirs []string
		links       map[string]string
		// bad is the path, under the base, the error names; "" passes
		bad string
	}{
		{name: "not there"},
		{name: "plain", dirs: []string{"out/aws/1/eu-west-2", "out/report"}, files: []string{"out/.gitignore", "out/aws/1/eu-west-2/main.tf"}},
		{name: "a link to a file under the root", dirs: []string{"out/aws/1", "out/common"}, files: []string{"out/common/providers.tf"}, links: map[string]string{"out/aws/1/providers.tf": "out/common/providers.tf"}},
		{name: "the root a link", dirs: []string{"real/aws"}, files: []string{"real/aws/main.tf"}, links: map[string]string{"out": "real"}},
		{name: "the root a file", files: []string{"out"}, bad: "out"},
		{name: "a directory a link", dirs: []string{"out"}, links: map[string]string{"out/report": "dir"}, bad: "out/report"},
		{name: "a deep directory a link", dirs: []string{"out/aws/1"}, links: map[string]string{"out/aws/1/eu-west-2": "dir"}, bad: "out/aws/1/eu-west-2"},
		{name: "a directory a link under the root", dirs: []string{"out/aws/1"}, links: map[string]string{"out/aws/2": "out/aws/1"}, bad: "out/aws/2"},
		{name: "a file a link out", dirs: []string{"out/aws"}, links: map[string]string{"out/aws/imports.tf": "file"}, bad: "out/aws/imports.tf"},
		{name: "a dangling link", dirs: []string{"out/modules/x"}, links: map[string]string{"out/modules/x/main.tf": "missing"}, bad: "out/modules/x/main.tf"},
		{name: "a link in a skipped directory", dirs: []string{"out/aws/.terraform/providers"}, links: map[string]string{"out/aws/.terraform/providers/aws": "dir"}},
		{name: "a skipped directory a link", dirs: []string{"out/aws"}, links: map[string]string{"out/aws/.terraform": "dir"}, bad: "out/aws/.terraform"},
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
				to, ok := map[string]string{"file": secret, "dir": outside, "missing": filepath.Join(outside, "missing")}[target]
				if !ok {
					to = filepath.Join(base, target)
				}
				if err := os.Symlink(to, filepath.Join(base, link)); err != nil {
					t.Fatal(err)
				}
			}
			err := CheckContained(filepath.Join(base, "out"), ".terraform")
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
}

// MkdirPrivate creates a directory private, with its parents as
// os.MkdirAll does, and makes one that exists private too.
func TestMkdirPrivate(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("modes restrict nothing on Windows: see TestMkdirPrivateSetsAProtectedAccessList")
	}
	out := filepath.Join(t.TempDir(), "out")
	dir := filepath.Join(out, ".infraharvest")
	if err := MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]fs.FileMode{dir: 0o700, out: 0o755} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		// The umask may take from the parent's mode, never add to it.
		if got := info.Mode().Perm(); got&^want != 0 || (path == dir && got != want) {
			t.Errorf("%s: mode %v, want %v", path, got, want)
		}
	}

	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("an existing directory isn't made private: %v, %v", info.Mode(), err)
	}

	// A link isn't followed to make what it leads to private, nor is a
	// file taken for a directory.
	elsewhere := t.TempDir()
	if err := os.Chmod(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(out, "link")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if err := MkdirPrivate(link); err == nil {
		t.Error("a link was taken for a directory")
	}
	if info, err := os.Stat(elsewhere); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("what a link leads to was changed: %v, %v", info.Mode(), err)
	}
	file := filepath.Join(out, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MkdirPrivate(file); err == nil {
		t.Error("a file was taken for a directory")
	}
}
