// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package logging

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Sanitize returns a writer that writes to w what it is given with each
// control character shown as an escape, such as \x1b, except the line
// feed and the tab. Names and values read from a cloud reach the terminal
// in log lines and in the text a command prints, and a name holding an
// escape sequence could otherwise move the cursor, hide what was printed
// or set the terminal's title. The escape is shown rather than dropped, so
// that the name that holds it is seen to.
func Sanitize(w io.Writer) io.Writer {
	return sanitizer{w: w}
}

// sanitizer is the writer Sanitize returns.
type sanitizer struct {
	w io.Writer
}

// Write writes p with its control characters escaped, reporting the length
// of p as written.
func (s sanitizer) Write(p []byte) (int, error) {
	if !hasControl(p) {
		return s.w.Write(p)
	}
	if _, err := io.WriteString(s.w, Printable(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// hasControl reports whether p holds what Printable escapes: a control
// character or a byte that isn't UTF-8.
func hasControl(p []byte) bool {
	for i := 0; i < len(p); {
		r, size := utf8.DecodeRune(p[i:])
		if (r == utf8.RuneError && size == 1) || isControl(r) {
			return true
		}
		i += size
	}
	return false
}

// Printable returns s with each control character shown as an escape
// (see Sanitize).
func Printable(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			// Not UTF-8: a terminal that reads 8-bit characters would take a
			// byte such as 0x9b as a control.
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case isControl(r) && r < 0x80:
			fmt.Fprintf(&b, `\x%02x`, r)
		case isControl(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// isControl reports whether r is a control character a terminal acts on,
// or a character that changes the order text is shown in: not the line
// feed or the tab, which text holds.
func isControl(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		// The embeddings, overrides and isolates of bidirectional text.
		return true
	}
	return false
}
