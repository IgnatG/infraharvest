// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package tfrun

import (
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether the current user owns the file, as the
// directories this process's own runs create are. The system's temporary
// directory is shared, and another user's directory isn't ours to remove.
func ownedByCurrentUser(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return !ok || int(st.Uid) == os.Getuid()
}
