// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package mdtext writes text infraharvest doesn't control into Markdown.
package mdtext

import (
	"strings"
	"unicode"
)

// The Markdown reports become pull request bodies and job summaries, and
// hold text infraharvest doesn't control: tag keys and values, IDs, stack
// names, Terraform's and the clouds' errors. Such text could end a list
// item or a table row with a line break, hide what follows in an HTML
// comment, or mention people. Text from outside goes through Prose, or,
// where it is a name or an ID, Code.

// Prose returns text for a line of Markdown. Line breaks and other
// control characters are spaces. Outside the code spans the text has,
// which render as they are, < and > are entities, so that no HTML,
// comments included, comes through, and @ is followed by an empty HTML
// comment, which GitHub shows as nothing but which keeps @name from
// mentioning anyone. [, ], the ! of an image and | are escaped, so that
// [x](url) is no link, ![x](url) no image and | ends no table cell.
func Prose(s string) string {
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
			if i+1 < len(s) && escapable(s, i+1) {
				// The escape that follows would be escaped itself.
				b.WriteString(`\\`)
			} else {
				b.WriteByte(c)
			}
		case '[', ']', '|':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '!':
			if escapable(s, i) {
				b.WriteByte('\\')
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

// escapable reports whether Prose escapes the byte of s at i: [, ], | and
// the ! of an image, which would make [x](url) and ![x](url) a link and an
// image, and | a table cell's end.
func escapable(s string, i int) bool {
	switch s[i] {
	case '[', ']', '|':
		return true
	case '!':
		return i+1 < len(s) && s[i+1] == '['
	}
	return false
}

// Code returns s as a Markdown code span, whatever backticks it holds: its
// fence is longer than any run of them. Line breaks and other control
// characters are spaces. Inside a table, it still needs Cell.
func Code(s string) string {
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

// Cell escapes text for a Markdown table cell, code spans included. A |
// that is escaped already, as Prose does, stays as it is.
func Cell(text string) string {
	var b strings.Builder
	slashes := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '|' && slashes%2 == 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
		if c == '\\' {
			slashes++
		} else {
			slashes = 0
		}
	}
	return b.String()
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
