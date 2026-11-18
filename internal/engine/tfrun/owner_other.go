// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !unix

package tfrun

import "os"

// ownedByCurrentUser reports whether the current user owns the file. Where
// the system's temporary directory is the user's own, as on Windows, every
// directory in it is.
func ownedByCurrentUser(os.FileInfo) bool { return true }
