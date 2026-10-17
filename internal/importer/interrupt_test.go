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
	"time"

	"github.com/IgnatG/infraharvest/internal/report"
)

// The first signal cancels the context with the signal as the cause, and
// releases the handler, so that a second one stops the process.
func TestInterruptOnTheFirstSignal(t *testing.T) {
	signals := make(chan os.Signal, 1)
	released := make(chan struct{})
	ctx, stop := interruptOn(t.Context(), signals, func() { close(released) })
	defer stop()

	signals <- syscall.SIGTERM
	<-ctx.Done()
	var interrupt *Interrupt
	if !errors.As(context.Cause(ctx), &interrupt) || interrupt.Signal != syscall.SIGTERM {
		t.Fatalf("cause %v, want the SIGTERM", context.Cause(ctx))
	}
	select {
	case <-released:
	case <-time.After(10 * time.Second):
		t.Fatal("the handler is still there after the first signal")
	}
	stop() // releases only once
}

// A run a signal stopped exits as a shell reports a process the signal
// ended; one cancelled otherwise, or ending without an error, keeps its
// code.
func TestInterruptedExitCodes(t *testing.T) {
	interruptedBy := func(sig os.Signal) context.Context {
		signals := make(chan os.Signal, 1)
		ctx, stop := interruptOn(t.Context(), signals, func() {})
		t.Cleanup(stop)
		signals <- sig
		<-ctx.Done()
		return ctx
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
		want int
	}{
		{"interrupt", interruptedBy(os.Interrupt), context.Canceled, report.ExitInterrupted},
		{"SIGTERM", interruptedBy(syscall.SIGTERM), context.Canceled, report.ExitTerminated},
		{"a failure the interrupt caused", interruptedBy(os.Interrupt), &ExitError{Code: report.ExitIncomplete, Err: errors.New("service s3: stopped")}, report.ExitInterrupted},
		{"cancelled without a signal", canceled, context.Canceled, report.ExitCouldNotRun},
		{"not interrupted", t.Context(), errors.New("no credentials"), report.ExitCouldNotRun},
		{"interrupted after it finished", interruptedBy(os.Interrupt), nil, report.ExitOK},
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
	signals := make(chan os.Signal, 1)
	ctx, stop := interruptOn(t.Context(), signals, func() {})
	defer stop()
	signals <- os.Interrupt
	<-ctx.Done()

	err := run.finish(Interrupted(ctx, ctx.Err()))

	if code := ExitCode(err); code != report.ExitInterrupted {
		t.Fatalf("exit code %d, want %d (%v)", code, report.ExitInterrupted, err)
	}
	content, readErr := os.ReadFile(filepath.Join(out, report.Dir, "coverage.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var coverage report.Report
	if err := json.Unmarshal(content, &coverage); err != nil {
		t.Fatal(err)
	}
	if coverage.ExitCode != report.ExitInterrupted {
		t.Errorf("coverage.json: exit code %d, want %d", coverage.ExitCode, report.ExitInterrupted)
	}
}
