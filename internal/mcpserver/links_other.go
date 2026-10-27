// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !unix

package mcpserver

import "io/fs"

// hardLinked reports whether the file info describes has more than one
// name, a hard link to it: never here, as this platform's file info
// doesn't count them.
func hardLinked(fs.FileInfo) bool {
	return false
}
