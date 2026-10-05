// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

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
