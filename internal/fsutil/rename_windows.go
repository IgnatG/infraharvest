// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package fsutil

import (
	"errors"
	"math/rand/v2"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// renameTimeout is how long rename keeps retrying a file in use, as
// cmd/go's robustio does.
const renameTimeout = 2 * time.Second

// rename renames from to to, as os.Rename does, but retries, for up to
// renameTimeout, where Windows refuses it because to is in use at that
// moment: being replaced by another rename, as by writers of one file at
// once, such as accounts imported in parallel writing the output's
// .gitignore, or open in another program, such as a virus scanner.
// Each retry waits up to twice as long as the one before, at random, so
// that writers at once don't retry in step. Access denied for good, as
// to a directory there, fails likewise, once renameTimeout is up.
func rename(from, to string) error {
	deadline := time.Now().Add(renameTimeout)
	for wait := time.Millisecond; ; wait *= 2 {
		err := os.Rename(from, to)
		if err == nil || !inUse(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(min(rand.N(wait)+1, time.Until(deadline)))
	}
}

// inUse reports whether err is Windows refusing to touch a file because
// another handle on it, or another rename of it, is in the way.
func inUse(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
