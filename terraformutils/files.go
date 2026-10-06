// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package terraformutils

import (
	"io/fs"

	"github.com/IgnatG/infraharvest/internal/fsutil"
)

// SecretFilePerm is the permission of output that can contain secrets, such
// as saved inventories: only the owner may read it.
const SecretFilePerm fs.FileMode = 0o600

// WriteSecretFile writes data to path so that only the owner can read it.
// The data goes to a private temporary file that then replaces path (see
// fsutil.WriteFile), so a file left by an earlier run with wider
// permissions is replaced rather than rewritten in place, and the data is
// never readable by others.
func WriteSecretFile(path string, data []byte) error {
	return fsutil.WriteFile(path, data, SecretFilePerm)
}
