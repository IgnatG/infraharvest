// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package terraformutils

import (
	"io/fs"
	"os"
	"path/filepath"
)

// Permissions for generated output. State and plan files can contain
// secrets, so only their owner may read them.
const (
	DirPerm        fs.FileMode = 0o755
	FilePerm       fs.FileMode = 0o644
	SecretFilePerm fs.FileMode = 0o600
)

// WriteSecretFile writes data to path so that only the owner can read it.
// The data goes to a private temporary file that then replaces path, so a
// file left by an earlier run with wider permissions is replaced rather than
// rewritten in place, and the data is never readable by others.
func WriteSecretFile(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()
	if err = f.Chmod(SecretFilePerm); err != nil {
		_ = f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	// Flush before the rename, so a crash right after it cannot leave path
	// pointing at a truncated file.
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
