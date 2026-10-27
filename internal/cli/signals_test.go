// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/importer"
)

// The command's context ends with its parent's, and stop releases it.
func TestInterruptContext(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	ctx, stop := interruptContext(parent)
	defer stop()
	cancel()
	<-ctx.Done()

	ctx, stop = interruptContext(t.Context())
	stop()
	if ctx.Err() == nil {
		t.Error("stop left the context running")
	}
}

// The first signal cancels the context with the signal as the cause, and
// releases the handler, so that a second one stops the process.
func TestInterruptOnTheFirstSignal(t *testing.T) {
	signals := make(chan os.Signal, 1)
	released := make(chan struct{})
	ctx, stop := interruptOn(t.Context(), signals, func() { close(released) })
	defer stop()

	signals <- syscall.SIGTERM
	<-ctx.Done()
	var interrupt *importer.Interrupt
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
