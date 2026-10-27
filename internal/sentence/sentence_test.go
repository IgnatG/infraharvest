// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package sentence

import "testing"

func TestList(t *testing.T) {
	for _, tc := range []struct {
		words       []string
		conjunction string
		want        string
	}{
		{nil, "and", ""},
		{[]string{"aws"}, "or", "aws"},
		{[]string{"aws", "azure"}, "or", "aws or azure"},
		{[]string{"aws", "azure", "google"}, "and", "aws, azure and google"},
	} {
		if got := List(tc.words, tc.conjunction); got != tc.want {
			t.Errorf("List(%q, %q) = %q, want %q", tc.words, tc.conjunction, got, tc.want)
		}
	}
}
