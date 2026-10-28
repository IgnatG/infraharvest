// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/importer"
)

// The configuration file sets --policy as a list, each item a directory.
func TestPolicyFromConfig(t *testing.T) {
	config := writeConfig(t, "version: 1\nsettings:\n  policy: [policies/org, policies/team]\n")
	var got importer.Options
	cmd := fakeProviderCommand(func(o importer.Options) error { got = o; return nil })
	cmd.SetArgs([]string{"--config", config})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"policies/org", "policies/team"}; !slices.Equal(got.Policies, want) {
		t.Errorf("policies %v, want %v", got.Policies, want)
	}
}

// import and verify, which run the gate, fail before anything else when
// --policy asks for a check conftest isn't installed to make; discover,
// which runs none, doesn't.
func TestPolicyWithoutConftest(t *testing.T) {
	policy := t.TempDir()
	out := filepath.Join(t.TempDir(), "out")
	t.Setenv("PATH", t.TempDir())
	const want = "--policy: checking the roots against policies needs conftest on PATH"

	cmd := NewCmdRoot()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"verify", "--policy=" + policy, out})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("verify: got %v, want %q", err, want)
	}

	var name string
	for _, n := range providers.Names() {
		name = n
	}
	if name == "" {
		t.Skip("no provider is built in")
	}
	// What else the commands need isn't checked before --policy.
	if err := CheckRun(t.Context(), []string{"import", name, "--services=x", "--all", "--policy=" + policy, "--out=" + out}); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("import %s: got %v, want %q", name, err, want)
	}
	if err := CheckRun(t.Context(), []string{"discover", name, "--services=x", "--policy=" + policy, "--out=" + out}); err != nil && strings.Contains(err.Error(), "--policy") {
		t.Errorf("discover %s: got %v, want --policy left to import", name, err)
	}
}
