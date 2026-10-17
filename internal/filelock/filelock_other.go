// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package filelock

import "os"

// lock doesn't lock: infraharvest isn't released for this system, and
// files aren't locked on it.
func lock(*os.File) error {
	return nil
}

func unlock(*os.File) error {
	return nil
}
