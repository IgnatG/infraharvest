// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/IgnatG/infraharvest/internal/filelock"
)

// inUseFile is the file in a held directory whose lock its creator holds
// for as long as it uses the directory.
const inUseFile = ".infraharvest-in-use"

// held are the locks this process holds, by directory.
var (
	heldMu sync.Mutex
	held   = map[string]*filelock.Lock{}
)

// MkdirTempHeld creates a private directory in the system's temporary
// directory as os.MkdirTemp does, and holds a lock on a file in it until
// RemoveHeld: RemoveStaleSnapshots skips a directory whose lock is held,
// however old it is, so that another infraharvest process never sweeps one
// a long-lived process (the MCP server) or a long run still uses. The
// system releases the lock when the process ends, however it ends, which
// leaves the directory to be swept once it is old enough.
func MkdirTempHeld(pattern string) (string, error) {
	return mkdirTempHeld("", pattern)
}

func mkdirTempHeld(parent, pattern string) (string, error) {
	dir, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		return "", err
	}
	l, err := filelock.TryLock(filepath.Join(dir, inUseFile))
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	heldMu.Lock()
	held[dir] = l
	heldMu.Unlock()
	return dir, nil
}

// RemoveHeld releases the lock of a directory MkdirTempHeld made and
// removes it with what it holds. It removes any other directory too.
func RemoveHeld(dir string) {
	releaseHeld(dir)
	_ = os.RemoveAll(dir)
}

// releaseHeld releases the lock this process holds on dir, if any: a
// directory with an open file can't be removed on Windows.
func releaseHeld(dir string) {
	heldMu.Lock()
	l := held[dir]
	delete(held, dir)
	heldMu.Unlock()
	if l != nil {
		_ = l.Unlock()
	}
}

// inUse reports whether a process holds the lock of the directory at dir:
// one that never made it, such as a directory of an older version, isn't.
func inUse(dir string) bool {
	l, err := filelock.TryLock(filepath.Join(dir, inUseFile))
	if errors.Is(err, filelock.ErrLocked) {
		return true
	}
	if err == nil {
		_ = l.Unlock()
	}
	return false
}
