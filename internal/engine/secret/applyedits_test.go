// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package secret

import "testing"

func TestApplyEdits(t *testing.T) {
	src := []byte("0123456789")
	for _, tt := range []struct {
		name  string
		edits []edit
		want  string
	}{
		{"none", nil, "0123456789"},
		{"one", []edit{{2, 4, "ab"}}, "01ab456789"},
		{"in any order", []edit{{8, 10, "Z"}, {0, 1, "AA"}, {4, 4, "-"}}, "AA123-4567Z"},
		{"to nothing", []edit{{0, 5, ""}, {5, 10, ""}}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(applyEdits(src, tt.edits)); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
