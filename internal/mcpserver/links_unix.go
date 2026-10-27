// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package mcpserver

import (
	"io/fs"
	"syscall"
)

// hardLinked reports whether the file info describes has more than one
// name, a hard link to it.
func hardLinked(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}
