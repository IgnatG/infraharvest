// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package driftpolicy

import "testing"

func TestMatchPatternCompilesOnce(t *testing.T) {
	t.Parallel()
	for range 3 {
		if !matchPattern("tags.*", "tags.env") || matchPattern("tags.*", "other") {
			t.Fatal("tags.* matched wrongly")
		}
	}
	if _, ok := compiled.Load("tags.*"); !ok {
		t.Error("the compiled pattern isn't kept")
	}
	if !matchPattern("a*c*e", "abcde") || matchPattern("a*c*e", "abcdf") || !matchPattern("exact", "exact") {
		t.Error("pattern matching changed")
	}
}
