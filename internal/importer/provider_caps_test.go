// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"testing"

	goversion "github.com/hashicorp/go-version"
)

// The AWS provider's cap applies in either registry, and holds it below
// 6.74.0 only.
func TestProviderCaps(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"hashicorp/aws", "registry.terraform.io/hashicorp/aws", "registry.opentofu.org/hashicorp/aws"} {
		c, ok := capOf(source)
		if !ok {
			t.Fatalf("%s: no cap", source)
		}
		for v, excluded := range map[string]bool{"6.73.0": false, "6.73.9": false, "6.74.0": true, "6.75.1": true, "7.0.0": true} {
			if got := c.excludes(goversion.Must(goversion.NewVersion(v))); got != excluded {
				t.Errorf("%s excludes %s: %v, want %v", source, v, got, excluded)
			}
			if got := c.constraint().Check(goversion.Must(goversion.NewVersion(v))); got == excluded {
				t.Errorf("%s: constraint admits %s: %v", source, v, got)
			}
		}
	}
	for _, source := range []string{"hashicorp/azurerm", "hashicorp/google", "acme/aws-tools", "aws"} {
		if _, ok := capOf(source); ok {
			t.Errorf("%s: has a cap", source)
		}
	}
}
