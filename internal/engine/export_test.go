// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

// What only the tests use.

// scopeWordIndex is scopeWordIndexFrom from the start of s.
func scopeWordIndex(s, id string) int {
	return scopeWordIndexFrom(s, id, 0, testTraits)
}
