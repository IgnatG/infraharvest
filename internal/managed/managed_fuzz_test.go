// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package managed

import (
	"strings"
	"testing"
)

// Parsing any content doesn't panic, and what a state file adds is keyed
// "type id" and found at where.
func FuzzParse(f *testing.F) {
	f.Add([]byte(state))
	f.Add([]byte(`{"version": 3}`))
	f.Add([]byte(`{"version": 4, "resources": [{"mode": "managed", "type": "aws_iam_role", "instances": [{"attributes": {"id": "app"}}]}]}`))
	f.Add([]byte(`{"version": 4, "resources": [{"mode": "managed", "type": "", "instances": [{"attributes": {"id": "", "arn": "arn:aws:iam::1:role/x"}}, {}]}, {"mode": "data"}]}`))
	f.Add([]byte(`{"version": 4, "resources": null}`))
	f.Add([]byte(`{"version": "4"}`))
	f.Add([]byte("not state"))
	f.Fuzz(func(t *testing.T, content []byte) {
		const where = "fuzz/terraform.tfstate"
		r := Resources{}
		if err := r.Parse(content, where); err != nil {
			if len(r) != 0 {
				t.Errorf("a state that fails to parse added %v", r)
			}
			return
		}
		for key, at := range r {
			if !strings.Contains(key, " ") || at.Where != where {
				t.Errorf("entry %q at %q", key, at)
			}
		}
	})
}
