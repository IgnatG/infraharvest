// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"path/filepath"
	"sync"
	"testing"

	"github.com/IgnatG/infraharvest/internal/version"
)

// The MCP server checks tool calls at the same time, each through a tree
// of commands of its own (see CheckRun): the trees share no command or
// flag, as the race detector checks, and each prints to its own output.
func TestRootsBuiltConcurrently(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	checks := [][]string{
		{"import", "aws", "--services=vpc", "--all", "--out=" + out},
		{"discover", "azure", "--services=resource_graph", "--subscriptions=one", "--out=" + out},
		{"import", "google", "--services=iam", "--all", "--projects=one", "--out=" + out},
	}
	var wg sync.WaitGroup
	for i := range 9 {
		wg.Go(func() {
			if err := CheckRun(t.Context(), checks[i%len(checks)]); err != nil {
				t.Errorf("check %v: %v", checks[i%len(checks)], err)
			}
		})
		wg.Go(func() {
			cmd := NewCmdRoot()
			var stdout bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetArgs([]string{"version"})
			if err := cmd.Execute(); err != nil {
				t.Errorf("version: %v", err)
			}
			if want := "infraharvest " + version.Version + "\n"; stdout.String() != want {
				t.Errorf("version printed %q, want %q", stdout.String(), want)
			}
		})
	}
	wg.Wait()
}
