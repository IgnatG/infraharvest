// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package hclfile

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclwrite"
)

// DropBackend keeps a file formatted as it was: formatted where it was,
// else as it is but for the block it removes.
func TestDropBackend(t *testing.T) {
	t.Parallel()
	formatted := "terraform {\n  required_version = \">= 1.5\"\n\n  backend \"s3\" {}\n}\n"
	if got := string(DropBackend([]byte(formatted))); strings.Contains(got, "backend") || got != string(hclwrite.Format([]byte(got))) {
		t.Errorf("formatted: %q", got)
	}
	unformatted := "terraform {\n  required_version=\">= 1.5\"\n  backend \"s3\" {}\n}\n"
	if got := string(DropBackend([]byte(unformatted))); strings.Contains(got, "backend") || !strings.Contains(got, "required_version=") {
		t.Errorf("unformatted: %q", got)
	}
	none := "provider \"aws\" {}\n"
	if got := string(DropBackend([]byte(none))); got != none {
		t.Errorf("without a backend: %q", got)
	}
}
