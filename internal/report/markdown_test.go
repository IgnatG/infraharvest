// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"strings"
	"testing"
)

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
