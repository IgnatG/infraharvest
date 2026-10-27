// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package fsutil writes files that must never be left half written, and
// checks that a directory a run writes into holds nothing that would lead
// those writes elsewhere.
package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// WriteFile writes data to the file at path, as os.WriteFile does, but
// atomically: through a temporary file in the same directory, renamed over
// path once complete. An import that is interrupted, or fails to write,
// leaves the previous file whole, never a truncated one: a selection file
// someone edited, a checkpoint a resumed import reads, a root's
// configuration.
func WriteFile(path string, data []byte, perm fs.FileMode) (err error) {
	// CreateTemp makes the file private (0600) until perm is set, once
	// the data is complete.
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// After closing: Windows can't change an open file's mode.
	if err = os.Chmod(f.Name(), perm); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// CheckTree checks that root, if it exists, is a directory, and that
// everything under it is a directory or a regular file, and fails naming
// the first path that isn't. A symbolic link there, such as one a cloned
// repository commits, would have writing, truncating or removing under
// root follow it to a file of the user's elsewhere, and a junction on
// Windows likewise. The contents of the directories named one of skip,
// such as .terraform, where terraform init links providers from the
// plugin cache, aren't checked, only the directories themselves.
func CheckTree(root string, skip ...string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return untrusted(root, info.Mode(), "a directory")
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch mode := d.Type(); {
		case mode.IsDir():
			if path != root && slices.Contains(skip, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		case mode.IsRegular():
			return nil
		default:
			return untrusted(path, mode, "a directory or a regular file")
		}
	})
}

// untrusted is the error of CheckTree and CheckFile for path, of mode,
// where they want what want says.
func untrusted(path string, mode fs.FileMode, want string) error {
	if mode&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link, not %s", path, want)
	}
	return fmt.Errorf("%s isn't %s", path, want)
}

// CheckFile checks that the file at path, if it exists, is a regular
// file, as CheckTree does for a whole directory.
func CheckFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return untrusted(path, info.Mode(), "a regular file")
	}
	return nil
}
