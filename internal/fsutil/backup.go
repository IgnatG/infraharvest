// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Backup holds files' contents and permissions to restore; nil contents
// for files that didn't exist.
type Backup struct {
	dir   string
	files map[string][]byte
	modes map[string]fs.FileMode
}

// BackupFiles reads the files names, by path in dir, to restore later.
func BackupFiles(dir string, names ...string) (*Backup, error) {
	b := &Backup{dir: dir, files: map[string][]byte{}, modes: map[string]fs.FileMode{}}
	for _, name := range names {
		path := filepath.Join(dir, name)
		content, err := os.ReadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			b.files[name] = nil
		case err != nil:
			return nil, err
		default:
			info, err := os.Stat(path)
			if err != nil {
				return nil, err
			}
			b.files[name] = content
			b.modes[name] = info.Mode().Perm()
		}
	}
	return b, nil
}

// Mode returns the permissions name had when it was backed up, or 0644 for
// a file that didn't exist.
func (b *Backup) Mode(name string) fs.FileMode {
	if mode, ok := b.modes[name]; ok {
		return mode
	}
	return 0o644
}

// Restore writes the files back as they were, with their permissions, and
// removes those that didn't exist.
func (b *Backup) Restore() error {
	for name, content := range b.files {
		path := filepath.Join(b.dir, name)
		if content == nil {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		if err := WriteFile(path, content, b.Mode(name)); err != nil {
			return err
		}
	}
	return nil
}
