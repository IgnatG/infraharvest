// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/IgnatG/infraharvest/internal/report"
)

// Interrupt is the cause of a command's context cancelled by a signal,
// which the command line catches for the whole command.
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
// with an *Interrupt as the cause. An interrupt shows as the context's
// cancellation, or as what failed because of it, so any error counts.
func Interrupted(ctx context.Context, err error) error {
	var interrupt *Interrupt
	if err == nil || !errors.As(context.Cause(ctx), &interrupt) {
		return err
	}
	return &ExitError{Code: interrupt.ExitCode(), Err: fmt.Errorf("%w: %w", interrupt, err)}
}
