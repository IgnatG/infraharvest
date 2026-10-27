// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lock takes f's exclusive flock without waiting.
func lock(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EWOULDBLOCK) {
			return ErrLocked
		}
		return err
	}
}

// unlock releases f's flock.
func unlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

// open opens the file at path for TryLock, creating it if need be, but
// not through a symbolic link: one put in its place since TryLock checked
// it fails the open.
func open(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW, 0o644)
}
