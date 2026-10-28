// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/filelock"
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
	tf := WithTimeout(&hangingTerraform{}, 10*time.Millisecond)
	_, err := tf.PlanJSON(context.Background(), io.Discard)
	var timeout *timeoutError
	if !errors.As(err, &timeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want a timeout, got %v", err)
	}
	if !strings.Contains(err.Error(), "timed out after 10ms") {
		t.Errorf("error: %v", err)
	}
	if again := WithTimeout(tf, time.Hour); again != tf {
		t.Error("bounded twice")
	}
}

// A command the caller cancels fails with the cancellation, not a timeout.
func TestCommandTimeoutLeavesCancellationAlone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := WithTimeout(&hangingTerraform{}, time.Hour).PlanJSON(ctx, io.Discard)
	var timeout *timeoutError
	if !errors.Is(err, context.Canceled) || errors.As(err, &timeout) {
		t.Errorf("want the cancellation, got %v", err)
	}
}

func TestNoCommandTimeout(t *testing.T) {
	tf := &hangingTerraform{}
	if got := WithTimeout(tf, 0); got != Terraform(tf) {
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
	if err := Init(ctx, tf); !errors.Is(err, context.DeadlineExceeded) {
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
	if diags, _, err := Plan(ctx, stoppedPlan{err: exit}); !errors.Is(err, context.Canceled) || diags != nil {
		t.Errorf("cancelled: got %v, %v; want the cancellation", diags, err)
	}

	timeout := &timeoutError{timeout: time.Minute}
	if diags, _, err := Plan(context.Background(), stoppedPlan{err: timeout}); !errors.Is(err, timeout) || diags != nil {
		t.Errorf("timed out: got %v, %v; want the timeout", diags, err)
	}

	// A plan that failed on its own reports its errors as diagnostics.
	if diags, _, err := Plan(context.Background(), stoppedPlan{err: exit}); err != nil || len(diags) != 1 {
		t.Errorf("failed: got %v, %v; want the diagnostic", diags, err)
	}
}

// Another process's lock on the plugin cache, a file next to it, makes an
// init wait, until its context is done; once it is released the lock is
// taken.
func TestLockPluginCache(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "plugins")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	other, err := filelock.TryLock(cache + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if lock, err := lockPluginCache(ctx, cache); !errors.Is(err, context.DeadlineExceeded) {
		if lock != nil {
			_ = lock.Unlock()
		}
		t.Fatalf("want the wait stopped by the context, got %v", err)
	}
	if err := other.Unlock(); err != nil {
		t.Fatal(err)
	}
	lock, err := lockPluginCache(t.Context(), cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 0 {
		t.Errorf("the plugin cache holds %v (%v), want nothing of the lock's", entries, err)
	}
}

func TestBinaryFind(t *testing.T) {
	versions := map[string]string{}
	versionOf := func(_ context.Context, path string) (*version.Version, error) {
		v, ok := versions[path]
		if !ok {
			return nil, errors.New("not an engine binary")
		}
		return version.NewVersion(v)
	}
	installed := ""
	terraform := TerraformBinary
	terraform.install = func(_ context.Context, dir string) (string, error) {
		if _, err := os.Stat(dir); err != nil {
			return "", err // find must create the cache directory first
		}
		installed = filepath.Join(dir, terraform.fileName())
		return installed, nil
	}
	t.Setenv("PATH", t.TempDir()) // no engine on PATH

	t.Run("explicit path too old", func(t *testing.T) {
		versions["/old/terraform"] = "1.4.6"
		if _, err := terraform.find(context.Background(), "/old/terraform", t.TempDir(), versionOf); err == nil {
			t.Error("want an error for Terraform older than 1.5")
		}
	})

	t.Run("cached binary is reused", func(t *testing.T) {
		cache := t.TempDir()
		cached := filepath.Join(cache, terraform.fileName())
		if err := os.WriteFile(cached, nil, 0o755); err != nil {
			t.Fatal(err)
		}
		versions[cached] = "1.16.5"
		installed = ""

		got, err := terraform.find(context.Background(), "", cache, versionOf)

		if err != nil || got != cached || installed != "" {
			t.Errorf("got %q, %v (installed %q); want the cached binary without installing", got, err, installed)
		}
	})

	t.Run("installs when nothing qualifies", func(t *testing.T) {
		cache := filepath.Join(t.TempDir(), "not-yet-created")

		got, err := terraform.find(context.Background(), "", cache, versionOf)

		if err != nil || got != filepath.Join(cache, terraform.fileName()) {
			t.Errorf("got %q, %v; want a fresh install into the cache", got, err)
		}
	})

	t.Run("OpenTofu is not downloaded", func(t *testing.T) {
		_, err := TofuBinary.find(context.Background(), "", t.TempDir(), versionOf)

		if err == nil || !strings.Contains(err.Error(), "tofu 1.6.0 or newer not found") {
			t.Errorf("want an error asking to install OpenTofu, got %v", err)
		}
	})

	t.Run("OpenTofu older than 1.6", func(t *testing.T) {
		versions["/old/tofu"] = "1.5.7"
		if _, err := TofuBinary.find(context.Background(), "/old/tofu", t.TempDir(), versionOf); err == nil {
			t.Error("want an error for OpenTofu older than 1.6")
		}
	})
}
