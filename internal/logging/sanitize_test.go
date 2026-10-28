// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package logging

import (
	"bytes"
	"fmt"
	"testing"
)

func TestPrintable(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ in, want string }{
		"plain":           {"bucket-1 (eu-west-2)\n\tnext", "bucket-1 (eu-west-2)\n\tnext"},
		"unicode":         {"ключ — 日本語", "ключ — 日本語"},
		"an escape":       {"x\x1b[2J\x1b]0;title\x07", `x\x1b[2J\x1b]0;title\x07`},
		"a carriage":      {"ok\rfail", `ok\x0dfail`},
		"a delete":        {"a\x7fb", `a\x7fb`},
		"a C1 control":    {"a\u009bb\u0085", `a\u009bb\u0085`},
		"an invalid byte": {"a\xffb", `a\xffb`},
		"a lone C1 byte":  {"a\x9bb", `a\x9bb`},
		"a null":          {"a\x00b", `a\x00b`},
		"nothing":         {"", ""},
	} {
		if got := Printable(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

// The writer reports the length it was given, not the length it wrote, so
// that callers such as the logger see a whole write.
func TestSanitizeReportsTheInputWritten(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	in := []byte("name \x1b[31mred\x1b[0m\n")
	n, err := Sanitize(&out).Write(in)
	if err != nil || n != len(in) {
		t.Fatalf("wrote %d, %v; want %d", n, err, len(in))
	}
	if want := `name \x1b[31mred\x1b[0m` + "\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}

// The characters that reorder displayed text, which could make a name read
// as another, are shown as escapes too.
func TestPrintableEscapesBidirectionalControls(t *testing.T) {
	t.Parallel()
	for _, r := range []rune{0x202a, 0x202e, 0x2066, 0x2069} {
		in := "gpj.exe" + string(r) + "txt"
		want := fmt.Sprintf(`gpj.exe\u%04xtxt`, r)
		if got := Printable(in); got != want {
			t.Errorf("U+%04X: got %q, want %q", r, got, want)
		}
	}
}
