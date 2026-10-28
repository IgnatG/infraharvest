// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Backup holds files' contents to restore; nil for files that didn't
// exist.
type Backup struct {
	dir   string
	files map[string][]byte
}

// BackupFiles reads the files names, by path in dir, to restore later.
func BackupFiles(dir string, names ...string) (*Backup, error) {
	b := &Backup{dir: dir, files: map[string][]byte{}}
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(dir, name))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			b.files[name] = nil
		case err != nil:
			return nil, err
		default:
			b.files[name] = content
		}
	}
	return b, nil
}

// Restore writes the files back as they were, and removes those that
// didn't exist.
func (b *Backup) Restore() error {
	for name, content := range b.files {
		path := filepath.Join(b.dir, name)
		if content == nil {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		if err := WriteFile(path, content, 0o644); err != nil {
			return err
		}
	}
	return nil
}
