// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !unix && !windows

package mcpserver

import (
	"io/fs"
	"os"
)

// hardLinked reports whether the open file f has more than one name, a
// hard link to it: never here, as this platform's file info doesn't count
// them.
func hardLinked(*os.File, fs.FileInfo) (bool, error) {
	return false, nil
}

// unfollowedLink reports whether the file at path, whose info from
// os.Lstat is info, is a link resolve doesn't follow: one of a type the
// platform has but Go doesn't know, fs.ModeIrregular.
func unfollowedLink(_ string, info fs.FileInfo) (bool, error) {
	return info.Mode()&fs.ModeIrregular != 0, nil
}
