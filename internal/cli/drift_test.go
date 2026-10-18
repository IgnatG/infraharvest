// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestDriftFlags(t *testing.T) {
	t.Parallel()
	cmd := newDriftCmd()
	for name, def := range map[string]string{
		"engine":            "terraform",
		"terraform-path":    "",
		"engine-timeout":    "1h0m0s",
		"format":            "text",
		"parallel":          "1",
		"root":              "[]",
		"detailed-exitcode": "false",
		"force-unlock":      "false",
	} {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Errorf("drift has no --%s", name)
			continue
		}
		if f.DefValue != def {
			t.Errorf("--%s defaults to %q, want %q", name, f.DefValue, def)
		}
	}
	// Detection only, for now.
	for _, name := range []string{"accept", "keep", "decisions", "write-decisions"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Errorf("drift has --%s", name)
		}
	}
}

func TestDriftRejectsBadFlags(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"--format":   {"--format", "yaml"},
		"--parallel": {"--parallel", "0"},
		"--engine":   {"--engine", "pulumi"},
		"--root":     {"--root", "aws/["},
		"arguments":  {"a", "b"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cmd := NewCmdRoot()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(append([]string{"drift", t.TempDir()}, args...))
			err := cmd.Execute()
			if err == nil {
				t.Fatal("drift ran")
			}
			if name != "arguments" && !strings.Contains(err.Error(), name) {
				t.Errorf("the error doesn't name %s: %v", name, err)
			}
		})
	}
}

func TestDriftWithoutRoots(t *testing.T) {
	t.Parallel()
	cmd := NewCmdRoot()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"drift", t.TempDir()})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "no generated roots") {
		t.Errorf("got %v, want no generated roots", err)
	}
}
