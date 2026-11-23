// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import "testing"

// privateTempDir points the system temporary directory, where snapshots go,
// at a directory of the test's own. os.TempDir reads TMPDIR on Unix but TMP
// and TEMP on Windows, so setting TMPDIR alone left Windows tests sharing
// the runner's temporary directory with every other test.
func privateTempDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, dir)
	}
}
