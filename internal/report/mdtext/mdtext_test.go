// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package mdtext

import "testing"

func TestProse(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain text, as it was", "plain text, as it was"},
		{"line one\nline two\r\n", "line one line two  "},
		{"<!-- hidden -->", "&lt;!-- hidden --&gt;"},
		{"ping @someone", "ping @<!-- -->someone"},
		// infraharvest's own code spans render as they are.
		{"set `TF_VAR_<name>` for @x", "set `TF_VAR_<name>` for @<!-- -->x"},
		{"a ``span ` with`` one", "a ``span ` with`` one"},
		// A backtick that closes nothing opens no code span.
		{"stray ` then <b>", "stray ` then &lt;b&gt;"},
		{"escaped \\` then <b>`", "escaped \\` then &lt;b&gt;`"},
		// An escaped backslash leaves the backtick after it opening a
		// code span, as Markdown reads it.
		{"\\\\`x` <!--", "\\\\`x` &lt;!--"},
		{"\\\\` <!-- `", "\\\\` <!-- `"},
	}
	for _, tt := range tests {
		if got := Prose(tt.in); got != tt.want {
			t.Errorf("Prose(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCode(t *testing.T) {
	tests := []struct{ in, want string }{
		{"i-0123", "`i-0123`"},
		{"a`b", "``a`b``"},
		{"``x", "``` ``x ```"},
		{"two\nlines", "`two lines`"},
		{"<!-- @x -->", "`<!-- @x -->`"},
		{"", "`  `"},
	}
	for _, tt := range tests {
		if got := Code(tt.in); got != tt.want {
			t.Errorf("Code(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := Cell(Code("a|b")); got != "`a\\|b`" {
		t.Errorf("Cell(Code(a|b)) = %q", got)
	}
}
