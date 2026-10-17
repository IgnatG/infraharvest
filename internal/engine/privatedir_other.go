// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !windows

package engine

import (
	"io/fs"
	"os"
)

// makePrivate makes dir private to the user running infraharvest, mode
// 0700, and returns the function that gives it its mode back.
func makePrivate(dir string) (restore func() error, err error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return func() error {
		return os.Chmod(dir, info.Mode()&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky))
	}, nil
}
