// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !windows

package fsutil

import "os"

// rename renames from to to: elsewhere than on Windows, a rename replaces
// a file whatever else has it open or is renaming over it.
func rename(from, to string) error {
	return os.Rename(from, to)
}
