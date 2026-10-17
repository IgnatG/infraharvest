// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockedByte is the offset of the one byte lock locks, as its high 32
// bits: far past what the file holds. Windows locks keep other processes
// from reading what they lock, and ReadHolder must read the holder.
const lockedByte = 1

// lock takes an exclusive lock on f without waiting.
func lock(f *os.File) error {
	ol := &windows.Overlapped{OffsetHigh: lockedByte}
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLocked
	}
	return err
}

// unlock releases the lock lock took on f.
func unlock(f *os.File) error {
	ol := &windows.Overlapped{OffsetHigh: lockedByte}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
