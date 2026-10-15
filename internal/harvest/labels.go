// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"strings"
	"unicode/utf8"
)

// labelPrefix starts every label that Label makes, so that a label never
// starts with a digit or a dash whatever the name it comes from.
const labelPrefix = "r--"

// escapeDigits is the minimum number of hexadecimal digits in an escape, so
// that a single-byte character is written as four digits like a two-byte one.
const escapeDigits = 4

// upperHex are the hexadecimal digits of an escape.
const upperHex = "0123456789ABCDEF"

// Label turns a name as listed into a Terraform label. ASCII letters,
// digits, underscores and dashes are kept. Every other character becomes a
// dash, the uppercase hexadecimal of its UTF-8 bytes (at least four digits,
// zero-padded) and another dash, so "my queue" becomes "my-0020-queue" and
// different names never give the same label. A byte that is not valid
// UTF-8 is escaped on its own. The label always starts with "r--", even
// for an empty name or one that already starts so.
//
// The result is plain ASCII and depends on name only.
func Label(name string) string {
	var label strings.Builder
	label.Grow(len(labelPrefix) + len(name))
	label.WriteString(labelPrefix)
	for i := 0; i < len(name); {
		if isLabelByte(name[i]) {
			label.WriteByte(name[i])
			i++
			continue
		}
		// DecodeRuneInString gives the length of a valid UTF-8 character,
		// and 1 for a byte that does not start one.
		_, size := utf8.DecodeRuneInString(name[i:])
		writeEscape(&label, name[i:i+size])
		i += size
	}
	return label.String()
}

// isLabelByte reports whether c may appear in a label as it is. Bytes of
// multi-byte UTF-8 characters are all above 0x7F, so they never qualify.
func isLabelByte(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	default:
		return c == '_' || c == '-'
	}
}

// writeEscape writes the escape of the character whose bytes are char.
func writeEscape(label *strings.Builder, char string) {
	label.WriteByte('-')
	for digits := 2 * len(char); digits < escapeDigits; digits++ {
		label.WriteByte('0')
	}
	for i := 0; i < len(char); i++ {
		label.WriteByte(upperHex[char[i]>>4])
		label.WriteByte(upperHex[char[i]&0x0F])
	}
	label.WriteByte('-')
}
