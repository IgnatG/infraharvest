// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package fsutil writes files that must never be left half written.
package fsutil

import (
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFile writes data to the file at path, as os.WriteFile does, but
// atomically: through a temporary file in the same directory, renamed over
// path once complete. An import that is interrupted, or fails to write,
// leaves the previous file whole, never a truncated one: a selection file
// someone edited, a checkpoint a resumed import reads, a root's
// configuration.
func WriteFile(path string, data []byte, perm fs.FileMode) (err error) {
	// CreateTemp makes the file private (0600): perm can only widen it,
	// once the data is complete.
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
