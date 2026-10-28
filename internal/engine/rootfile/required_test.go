// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package rootfile

import (
	"testing"

	"github.com/hashicorp/go-version"
)

func TestRequiredVersionAndProviderConstraint(t *testing.T) {
	t.Parallel()
	v := version.Must(version.NewVersion("1.16.5"))
	if got, want := RequiredVersion(v), ">= 1.16, < 2.0"; got != want {
		t.Errorf("RequiredVersion: got %q, want %q", got, want)
	}
	if got, want := ProviderConstraint(version.Must(version.NewVersion("6.67.2"))), "~> 6.67"; got != want {
		t.Errorf("ProviderConstraint: got %q, want %q", got, want)
	}
}

func TestLowerBound(t *testing.T) {
	t.Parallel()
	for constraint, want := range map[string]string{
		"~> 6.14":        ">= 6.14",
		">= 1.5, < 2.0":  ">= 1.5",
		"1.16.5":         ">= 1.16.5",
		">= 1.5, >= 1.7": ">= 1.7",
		"> 1.5, != 1.6":  ">= 1.5",
	} {
		got, err := LowerBound(constraint)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v, want %q", constraint, got, err, want)
		}
	}
	for _, constraint := range []string{"< 2.0", "not a version"} {
		if got, err := LowerBound(constraint); err == nil {
			t.Errorf("%q: got %q, want an error", constraint, got)
		}
	}
}
