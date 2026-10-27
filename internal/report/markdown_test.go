// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"strings"
	"testing"
)

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
		if got := prose(tt.in); got != tt.want {
			t.Errorf("prose(%q) = %q, want %q", tt.in, got, tt.want)
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
		if got := code(tt.in); got != tt.want {
			t.Errorf("code(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := cell(code("a|b")); got != "`a\\|b`" {
		t.Errorf("cell(code(a|b)) = %q", got)
	}
}

// Text from the cloud stays on its line, mentions no one and hides
// nothing that follows it.
func TestMarkdownCloudText(t *testing.T) {
	const evil = "x`\n\n## Injected\n<!--\n@someone |"
	r := &Report{
		Tags:     []TagCount{{Key: "team", Value: evil, CoverageTotal: CoverageTotal{Discovered: 1}}},
		Excluded: []Excluded{{Type: "aws_s3_bucket", ID: evil, Reason: evil}},
		Warnings: []string{evil},
		Failures: []string{evil},
	}
	md := r.Markdown()
	for _, bad := range []string{"\n## Injected", "<!--\n", "\n@someone"} {
		if strings.Contains(md, bad) {
			t.Errorf("the report holds %q:\n%s", bad, md)
		}
	}
	for _, want := range []string{"\n## Excluded\n", "\n## Failures\n", "| ``x`  ## Injected <!-- @someone \\|`` | 1 |", "&lt;!-- @<!-- -->someone"} {
		if !strings.Contains(md, want) {
			t.Errorf("the report lacks %q:\n%s", want, md)
		}
	}
}
