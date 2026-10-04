// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package terraformutils

import "testing"

func TestTerraform13Adjustments(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"a provider requirement": {
			in:   "terraform {\n\trequired_providers \"aws\" {\n\t\tsource = \"hashicorp/aws\"\n\t}\n}\n",
			want: "terraform {\n\trequired_providers {\n\t\taws = {\n\t\t\tsource = \"hashicorp/aws\"\n\t\t}\n\t}\n}\n",
		},
		"an empty requirement": {
			in:   "terraform {\n\trequired_providers \"aws\" {\n\t}\n}\n",
			want: "terraform {\n\trequired_providers {\n\t\taws = {\n\t\t}\n\t}\n}\n",
		},
		// B-17: a missing closing brace used to panic.
		"no closing brace": {
			in:   "terraform {\n\trequired_providers \"aws\" {\n\t\tsource = \"hashicorp/aws\"",
			want: "terraform {\n\trequired_providers \"aws\" {\n\t\tsource = \"hashicorp/aws\"",
		},
		"nothing to adjust": {
			in:   "provider \"aws\" {\n}\n",
			want: "provider \"aws\" {\n}\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := string(terraform13Adjustments([]byte(tc.in))); got != tc.want {
				t.Errorf("got\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}
