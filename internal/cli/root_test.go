// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/IgnatG/infraharvest/internal/cli/providers"
	"github.com/IgnatG/infraharvest/internal/version"
)

// The MCP server checks tool calls at the same time, each through a tree
// of commands of its own (see CheckRun): the trees share no command or
// flag, as the race detector checks, and each prints to its own output.
func TestRootsBuiltConcurrently(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	// A build with -tags minimal checks only the providers it includes.
	var checks [][]string
	for _, check := range [][]string{
		{"import", "aws", "--services=vpc", "--all", "--out=" + out},
		{"discover", "azure", "--services=resource_graph", "--subscriptions=one", "--out=" + out},
		{"import", "google", "--services=iam", "--all", "--projects=one", "--out=" + out},
	} {
		if slices.Contains(providers.Names(), check[1]) {
			checks = append(checks, check)
		}
	}
	if len(checks) == 0 {
		t.Skip("no provider is built in")
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

// A command only a build with AWS has, such as cfn, is compiled out of a
// build without it: the file that adds it to awsCommands carries the
// build constraint the AWS provider command does.
func TestAWSCommandsHaveBuildConstraint(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "awsCommands = append(") {
			continue
		}
		found = true
		if !strings.Contains(string(src), "\n//go:build !minimal || aws\n") {
			t.Errorf("%s: missing //go:build !minimal || aws", f)
		}
	}
	if !found {
		t.Error("no file adds an AWS command, but cfn is one")
	}
}
