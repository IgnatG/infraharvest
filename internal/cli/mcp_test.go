// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestHelperLogs is the child process of TestExecRunnerKeepsTheLastLines:
// it logs 50 lines and prints a report. It does nothing in a normal test
// run.
func TestHelperLogs(t *testing.T) {
	if os.Getenv("INFRAHARVEST_TEST_LOGS") == "" {
		t.Skip("a helper for TestExecRunnerKeepsTheLastLines")
	}
	for i := range 50 {
		fmt.Fprintf(os.Stderr, "step %d\n", i)
	}
	fmt.Print(`{"done":true}`)
	os.Exit(1)
}

// The runner returns the child's report, its exit code and only the last
// lines it logged, and passes each line to progress as it comes.
func TestExecRunnerKeepsTheLastLines(t *testing.T) {
	t.Setenv("INFRAHARVEST_TEST_LOGS", "1")
	var lines []string
	stdout, stderr, code, err := execRunner(os.Args[0])(t.Context(), []string{"-test.run=^TestHelperLogs$"}, func(line string) { lines = append(lines, line) })
	if err != nil || code != 1 {
		t.Fatalf("got code %d, %v", code, err)
	}
	if !strings.HasPrefix(string(stdout), `{"done":true}`) {
		t.Errorf("stdout: %q", stdout)
	}
	kept := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	if len(kept) != 20 || kept[0] != "step 30" || kept[19] != "step 49" {
		t.Errorf("stderr kept: %q", kept)
	}
	if len(lines) != 50 || lines[0] != "step 0" {
		t.Errorf("progress got %d lines", len(lines))
	}
}

// TestHelperSleeps is the child process of TestExecRunnerKilled: it sleeps
// until killed. It does nothing in a normal test run.
func TestHelperSleeps(t *testing.T) {
	if os.Getenv("INFRAHARVEST_TEST_SLEEP") == "" {
		t.Skip("a helper for TestExecRunnerKilled")
	}
	time.Sleep(time.Minute)
}

// A child killed when the context ends has no exit code to report: the
// runner returns the context's error.
func TestExecRunnerKilled(t *testing.T) {
	t.Setenv("INFRAHARVEST_TEST_SLEEP", "1")
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	_, _, code, err := execRunner(os.Args[0])(ctx, []string{"-test.run=^TestHelperSleeps$"}, nil)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got code %d, %v; want %v", code, err, context.DeadlineExceeded)
	}
}

// A tool's child doesn't profile itself into the server's profile files.
func TestChildEnvLeavesOutProfiles(t *testing.T) {
	env := []string{"PATH=/usr/bin", "INFRAHARVEST_CPU_PROFILE=cpu.out", "INFRAHARVEST_MEM_PROFILE=mem.out", "INFRAHARVEST_GOROUTINELEAK_PROFILE=leak.out", "AWS_PROFILE=prod"}
	got := childEnv(env)
	if len(got) != 2 || got[0] != "PATH=/usr/bin" || got[1] != "AWS_PROFILE=prod" {
		t.Errorf("child environment: %v", got)
	}
}
