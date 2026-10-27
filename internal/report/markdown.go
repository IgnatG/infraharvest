// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"strings"
	"unicode"
)

// The Markdown reports become pull request bodies and job summaries, and
// hold text infraharvest doesn't control: tag keys and values, IDs, stack
// names, Terraform's and the clouds' errors. Such text could end a list
// item or a table row with a line break, hide what follows in an HTML
// comment, or mention people. Text from outside goes through prose, or,
// where it is a name or an ID, code.

// prose returns text for a line of Markdown. Line breaks and other
// control characters are spaces. Outside the code spans the text has,
// which render as they are, < and > are entities, so that no HTML,
// comments included, comes through, and @ is followed by an empty HTML
// comment, which GitHub shows as nothing but which keeps @name from
// mentioning anyone.
func prose(s string) string {
	s = flatten(s)
	var b strings.Builder
	for i := 0; i < len(s); {
		switch c := s[i]; c {
		case '\\':
			// An escaped backtick opens no code span, and an escaped
			// backslash escapes nothing after it: the pair goes as it is.
			if i+1 < len(s) && (s[i+1] == '`' || s[i+1] == '\\') {
				b.WriteString(s[i : i+2])
				i += 2
				continue
			}
			b.WriteByte(c)
		case '`':
			n := backticks(s[i:])
			end := i + n
			if closing := closingRun(s[end:], n); closing >= 0 {
				end += closing + n
			}
			b.WriteString(s[i:end])
			i = end
			continue
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '@':
			b.WriteString("@<!-- -->")
		default:
			b.WriteByte(c)
		}
		i++
	}
	return b.String()
}

// code returns s as a Markdown code span, whatever backticks it holds: its
// fence is longer than any run of them. Line breaks and other control
// characters are spaces. Inside a table, it still needs cell.
func code(s string) string {
	s = flatten(s)
	longest := 0
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		n := backticks(s[i:])
		longest = max(longest, n)
		i += n
	}
	fence := strings.Repeat("`", longest+1)
	if s == "" || strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		// Empty, the fences would be one run of backticks, which could
		// pair with others around it.
		// A space on each side keeps the fence apart from the text's own
		// backticks; Markdown drops it.
		s = " " + s + " "
	}
	return fence + s + fence
}

// cell escapes text for a Markdown table cell, code spans included.
func cell(text string) string {
	return strings.ReplaceAll(text, "|", `\|`)
}

// flatten returns s with each control character, line breaks included, a
// space.
func flatten(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// backticks returns the length of the run of backticks s starts with.
func backticks(s string) int {
	n := 0
	for n < len(s) && s[n] == '`' {
		n++
	}
	return n
}

// closingRun returns where in s a run of exactly n backticks starts, or -1.
func closingRun(s string, n int) int {
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		m := backticks(s[i:])
		if m == n {
			return i
		}
		i += m
	}
	return -1
}
