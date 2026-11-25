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

// provider_caps.json parses, and each entry names a release and says why.
func TestProviderCapsFile(t *testing.T) {
	t.Parallel()
	caps, err := parseProviderCaps(providerCapsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) == 0 {
		t.Fatal("no caps")
	}
	for source, c := range caps {
		if c.below == nil || c.reason == "" {
			t.Errorf("%s: incomplete cap %+v", source, c)
		}
		if c.below != nil && c.below.Prerelease() != "" {
			t.Errorf("%s: below %s is a prerelease", source, c.below)
		}
	}
	if got := caps["hashicorp/aws"].below.String(); got != "6.74.0" {
		t.Errorf("hashicorp/aws below %s, want 6.74.0", got)
	}
}

// A malformed file, release or entry is an error, not a cap.
func TestParseProviderCapsRejects(t *testing.T) {
	t.Parallel()
	for name, data := range map[string]string{
		"not JSON":        `[`,
		"not a version":   `{"hashicorp/aws": {"below": "six", "reason": "x"}}`,
		"no release":      `{"hashicorp/aws": {"reason": "x"}}`,
		"no reason":       `{"hashicorp/aws": {"below": "6.74.0"}}`,
		"no namespace":    `{"aws": {"below": "6.74.0", "reason": "x"}}`,
		"upper-case type": `{"hashicorp/AWS": {"below": "6.74.0", "reason": "x"}}`,
	} {
		if _, err := parseProviderCaps([]byte(data)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
