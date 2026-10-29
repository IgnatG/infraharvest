// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/IgnatG/infraharvest/internal/report"
)

// interruptedBy returns a context a signal cancelled, as the command
// line's does (see Interrupt).
func interruptedBy(t *testing.T, sig os.Signal) context.Context {
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(&Interrupt{Signal: sig})
	return ctx
}

// A run a signal stopped exits as a shell reports a process the signal
// ended; one cancelled otherwise, or ending without an error, keeps its
// code.
func TestInterruptedExitCodes(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
		want int
	}{
		{"interrupt", interruptedBy(t, os.Interrupt), context.Canceled, report.ExitInterrupted},
		{"SIGTERM", interruptedBy(t, syscall.SIGTERM), context.Canceled, report.ExitTerminated},
		{"a failure the interrupt caused", interruptedBy(t, os.Interrupt), &ExitError{Code: report.ExitIncomplete, Err: errors.New("service s3: stopped")}, report.ExitInterrupted},
		{"cancelled without a signal", canceled, context.Canceled, report.ExitCouldNotRun},
		{"not interrupted", t.Context(), errors.New("no credentials"), report.ExitCouldNotRun},
		{"interrupted after it finished", interruptedBy(t, os.Interrupt), nil, report.ExitOK},
	} {
		err := Interrupted(tc.ctx, tc.err)
		if code := ExitCode(err); code != tc.want {
			t.Errorf("%s: exit code %d, want %d (%v)", tc.name, code, tc.want, err)
		}
		if tc.err != nil && !errors.Is(err, tc.err) {
			t.Errorf("%s: %v lost the error %v", tc.name, err, tc.err)
		}
	}
}

// The report of an interrupted run has the exit code the process exits
// with.
func TestInterruptedRunReportsItsExitCode(t *testing.T) {
	out := t.TempDir()
	run := NewRun()
	run.used = true
	run.options = Options{Out: out}
	ctx := interruptedBy(t, os.Interrupt)

	err := run.finish(Interrupted(ctx, ctx.Err()))

	if code := ExitCode(err); code != report.ExitInterrupted {
		t.Fatalf("exit code %d, want %d (%v)", code, report.ExitInterrupted, err)
	}
	content, readErr := os.ReadFile(filepath.Join(out, report.Dir, "report.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var coverage report.Report
	if err := json.Unmarshal(content, &coverage); err != nil {
		t.Fatal(err)
	}
	if coverage.ExitCode != report.ExitInterrupted {
		t.Errorf("report.json: exit code %d, want %d", coverage.ExitCode, report.ExitInterrupted)
	}
}
