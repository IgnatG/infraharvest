// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

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

	_, _, code, err := execRunner(os.Args[0])(ctx, []string{"-test.run=^TestHelperSleeps$"})

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
