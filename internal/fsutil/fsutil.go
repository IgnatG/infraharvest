// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package fsutil writes files that must never be left half written,
// checks that a directory a run writes into holds nothing that would lead
// those writes elsewhere, and makes directories private to the user
// running infraharvest.
package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// WriteFile writes data to the file at path, as os.WriteFile does, but
// atomically: through a temporary file in the same directory, renamed over
// path once complete. An import that is interrupted, or fails to write,
// leaves the previous file whole, never a truncated one: a selection file
// someone edited, a checkpoint a resumed import reads, a root's
// configuration.
//
// On Windows, where a rename over a file another rename or program has
// in use at that moment is refused, the rename is retried briefly (see
// rename), so writers of one file at once each succeed.
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
	return rename(f.Name(), path)
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

// CheckContained checks that nothing under root, a directory a run writes
// into, leads a write elsewhere, as CheckTree does, but for a directory
// that may hold files of the user's as well as the run's, such as an
// output directory a repository commits: every directory under it is
// one, not a symbolic link to one or a junction; every symbolic link
// leads to a regular file under root; everything else is a regular file.
// A link to a directory there would have the run write, through it,
// files of the user's elsewhere, a dangling one have it create one
// elsewhere, and a link out of root have it read a file of the user's
// into one it writes, which a commit of the directory then carries.
// Root itself, and the directories it is in, may be links: they are the
// user's choice. The contents of the directories named one of skip aren't
// checked, only the directories themselves.
func CheckContained(root string, skip ...string) error {
	resolved, err := filepath.EvalSymlinks(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s isn't a directory", root)
	}
	return filepath.WalkDir(resolved, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Named under root, as the user gave it.
		rel, err := filepath.Rel(resolved, path)
		if err != nil {
			return err
		}
		name := filepath.Join(root, rel)
		switch mode := d.Type(); {
		case mode.IsDir():
			if path != resolved && slices.Contains(skip, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		case mode.IsRegular():
			return nil
		case mode&fs.ModeSymlink == 0:
			return untrusted(name, mode, "a directory or a regular file")
		}
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("%s is a symbolic link that leads to nothing there is", name)
		}
		info, err := os.Stat(target)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("%s is a symbolic link to a directory", name)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is a symbolic link to what isn't a regular file", name)
		}
		if r, err := filepath.Rel(resolved, target); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%s is a symbolic link out of %s", name, root)
		}
		return nil
	})
}

// MkdirPrivate creates dir, with its parents, and makes it private (see
// MakePrivate) as it creates it, so that what is written into it is
// private too, whatever its parent lets others read. A dir that exists
// already is made private too, as a run of an older version may have made
// it open; one that is a link, or isn't a directory, is refused.
func MkdirPrivate(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		info, statErr := os.Lstat(dir)
		if statErr != nil {
			return statErr
		}
		if !info.IsDir() {
			return fmt.Errorf("%s isn't a directory", dir)
		}
	}
	_, err := MakePrivate(dir)
	return err
}
