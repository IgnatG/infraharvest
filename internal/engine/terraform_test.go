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

// stoppedPlan is a Terraform whose plan reports an error diagnostic, as
// Terraform does when it is stopped part way, and fails with err.
type stoppedPlan struct {
	Terraform
	err error
}

func (s stoppedPlan) PlanJSON(_ context.Context, w io.Writer, _ ...tfexec.PlanOption) (bool, error) {
	_, _ = io.WriteString(w, `{"type":"diagnostic","diagnostic":{"severity":"error","summary":"Operation cancelled"}}`+"\n")
	return false, s.err
}

func (stoppedPlan) SetStdout(io.Writer) {}

// The diagnostics of a plan that was stopped aren't errors of the
// configuration: plan returns why it was stopped.
func TestPlanStopped(t *testing.T) {
	exit := errors.New("exit status 1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if diags, _, err := plan(ctx, stoppedPlan{err: exit}); !errors.Is(err, context.Canceled) || diags != nil {
		t.Errorf("cancelled: got %v, %v; want the cancellation", diags, err)
	}

	timeout := &timeoutError{timeout: time.Minute}
	if diags, _, err := plan(context.Background(), stoppedPlan{err: timeout}); !errors.Is(err, timeout) || diags != nil {
		t.Errorf("timed out: got %v, %v; want the timeout", diags, err)
	}

	// A plan that failed on its own reports its errors as diagnostics.
	if diags, _, err := plan(context.Background(), stoppedPlan{err: exit}); err != nil || len(diags) != 1 {
		t.Errorf("failed: got %v, %v; want the diagnostic", diags, err)
	}
}
