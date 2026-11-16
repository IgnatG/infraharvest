// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

// What only the tests use.

// mergedRootFiles is buildMergedRoot without the writer.
func mergedRootFiles(m *stackMerge) (map[string][]byte, error) {
	files, _, err := buildMergedRoot(m)
	return files, err
}

// Passed reports whether every environment's gate passed.
func (r *MergeResult) Passed() bool {
	for _, e := range r.Environments {
		if !e.Gate.Passed() {
			return false
		}
	}
	return r.Reason == ""
}
