// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package mcpserver

import (
	"io/fs"
	"os"
	"syscall"
)

// hardLinked reports whether the open file f, whose info is info, has
// more than one name, a hard link to it.
func hardLinked(_ *os.File, info fs.FileInfo) (bool, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1, nil
}

// unfollowedLink reports whether the file at path, whose info from
// os.Lstat is info, is a link resolve doesn't follow: one of a type the
// platform has but Go doesn't know, fs.ModeIrregular.
func unfollowedLink(_ string, info fs.FileInfo) (bool, error) {
	return info.Mode()&fs.ModeIrregular != 0, nil
}
