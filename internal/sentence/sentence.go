// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package sentence words lists as messages and help text read them.
package sentence

import "strings"

// List joins words as a sentence lists them, with conjunction, such as
// "and" or "or", before the last: a, b and c.
func List(words []string, conjunction string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " " + conjunction + " " + words[len(words)-1]
}
