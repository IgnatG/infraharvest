// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// hangingTerraform is a Terraform whose plans and inits run until their
// context is done, as one whose provider retries an unreachable endpoint.
type hangingTerraform struct {
	Terraform
	inits int
}

func (h *hangingTerraform) Init(ctx context.Context, _ ...tfexec.InitOption) error {
	h.inits++
	<-ctx.Done()
	return ctx.Err()
}

func (h *hangingTerraform) PlanJSON(ctx context.Context, _ io.Writer, _ ...tfexec.PlanOption) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func TestCommandTimeout(t *testing.T) {
	tf := withTimeout(&hangingTerraform{}, 10*time.Millisecond)
	_, err := tf.PlanJSON(context.Background(), io.Discard)
	var timeout *timeoutError
	if !errors.As(err, &timeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want a timeout, got %v", err)
	}
	if !strings.Contains(err.Error(), "timed out after 10ms") {
		t.Errorf("error: %v", err)
	}
	if again := withTimeout(tf, time.Hour); again != tf {
		t.Error("bounded twice")
	}
}

// A command the caller cancels fails with the cancellation, not a timeout.
func TestCommandTimeoutLeavesCancellationAlone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := withTimeout(&hangingTerraform{}, time.Hour).PlanJSON(ctx, io.Discard)
	var timeout *timeoutError
	if !errors.Is(err, context.Canceled) || errors.As(err, &timeout) {
		t.Errorf("want the cancellation, got %v", err)
	}
}

func TestNoCommandTimeout(t *testing.T) {
	tf := &hangingTerraform{}
	if got := withTimeout(tf, 0); got != Terraform(tf) {
		t.Errorf("got %T, want the Terraform itself", got)
	}
}

// A root waiting for another root's init stops waiting when its context
// is done, without running init.
func TestInitWaitStopsOnCancellation(t *testing.T) {
	initSlot <- struct{}{} // another root's init is running
	defer func() { <-initSlot }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	tf := &hangingTerraform{}
	if err := initTerraform(ctx, tf); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want the context's error, got %v", err)
	}
	if tf.inits != 0 {
		t.Errorf("init ran %d times while another was running", tf.inits)
	}
}
