// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/IgnatG/infraharvest/internal/report"
)

// InterruptContext returns ctx, cancelled on the first interrupt or
// SIGTERM, for a whole command: every Import call it makes stops, and the
// run still writes its report. The cause of the cancellation is an
// *Interrupt naming the signal, which Interrupted turns into the exit
// code. After that first signal the handler is removed, so a second one
// stops the process at once, as when a lister or a Terraform child
// doesn't stop. stop releases the handler.
func InterruptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	return interruptOn(ctx, signals, func() { signal.Stop(signals) })
}

// interruptOn returns ctx, cancelled with an *Interrupt by the first
// signal on signals, and the function that releases it; release stops the
// signals, after the first or when the context ends.
func interruptOn(ctx context.Context, signals <-chan os.Signal, release func()) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	var once sync.Once
	stop := func() {
		once.Do(release)
		cancel(context.Canceled)
	}
	go func() {
		select {
		case sig := <-signals:
			cancel(&Interrupt{Signal: sig})
		case <-ctx.Done():
		}
		stop()
	}()
	return ctx, stop
}

// Interrupt is the cause of a command's context cancelled by a signal
// (see InterruptContext).
type Interrupt struct {
	Signal os.Signal
}

func (i *Interrupt) Error() string {
	return "stopped: " + i.Signal.String()
}

// ExitCode returns the exit code of a run the signal stopped:
// report.ExitTerminated for SIGTERM, report.ExitInterrupted for an
// interrupt.
func (i *Interrupt) ExitCode() int {
	if i.Signal == syscall.SIGTERM {
		return report.ExitTerminated
	}
	return report.ExitInterrupted
}

// Interrupted returns err, the error a command run under ctx ends on,
// with the exit code of the signal that stopped it, if one cancelled ctx
// (see InterruptContext). An interrupt shows as the context's
// cancellation, or as what failed because of it, so any error counts.
func Interrupted(ctx context.Context, err error) error {
	var interrupt *Interrupt
	if err == nil || !errors.As(context.Cause(ctx), &interrupt) {
		return err
	}
	return &ExitError{Code: interrupt.ExitCode(), Err: fmt.Errorf("%w: %w", interrupt, err)}
}
