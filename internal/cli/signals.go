// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/IgnatG/infraharvest/internal/importer"
)

// interruptContext returns ctx, cancelled on the first interrupt or
// SIGTERM, for a whole command: every Import call it makes stops, and the
// run still writes its report. The cause of the cancellation is an
// *importer.Interrupt naming the signal, which importer.Interrupted turns
// into the exit code. After that first signal the handler is removed, so a
// second one stops the process at once, as when a lister or a Terraform
// child doesn't stop. stop releases the handler.
func interruptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	return interruptOn(ctx, signals, func() { signal.Stop(signals) })
}

// interruptOn returns ctx, cancelled with an *importer.Interrupt by the
// first signal on signals, and the function that releases it; release
// stops the signals, after the first or when the context ends.
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
			cancel(&importer.Interrupt{Signal: sig})
		case <-ctx.Done():
		}
		stop()
	}()
	return ctx, stop
}
