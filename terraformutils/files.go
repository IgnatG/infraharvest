// Copyright 2026 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
