// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package filelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A lock has one holder at a time, also within one process, and is free
// again once released.
func TestTryLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := TryLock(path); !errors.Is(err, ErrLocked) {
		if second != nil {
			_ = second.Unlock()
		}
		t.Fatalf("second lock: got %v, want ErrLocked", err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatal(err)
	}
	again, err := TryLock(path)
	if err != nil {
		t.Fatalf("after the unlock: %v", err)
	}
	if err := again.Unlock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the lock file should stay: %v", err)
	}
}

// The holder can be read while the lock is held, and is cleared when it
// is released.
func TestRecordAndReadHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	l, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadHolder(path); ok {
		t.Error("a holder before Record")
	}
	if err := l.Record(); err != nil {
		t.Fatal(err)
	}
	h, ok := ReadHolder(path)
	host, _ := os.Hostname()
	if !ok || h.PID != os.Getpid() || (host != "" && h.Host != host) || time.Since(h.Started) > time.Hour {
		t.Errorf("holder: %+v, %v", h, ok)
	}
	if err := l.Unlock(); err != nil {
		t.Fatal(err)
	}
	if h, ok := ReadHolder(path); ok {
		t.Errorf("holder after the unlock: %+v", h)
	}
	if _, ok := ReadHolder(filepath.Join(t.TempDir(), "missing")); ok {
		t.Error("a holder of a file that isn't there")
	}
}

// Wait takes the lock once its holder releases it, saying once that it
// waits, and stops waiting when its context is done.
func TestWait(t *testing.T) {
	interval := pollInterval
	pollInterval = time.Millisecond
	defer func() { pollInterval = interval }()
	path := filepath.Join(t.TempDir(), "lock")
	held, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if l, err := Wait(ctx, path, nil); !errors.Is(err, context.Canceled) {
		if l != nil {
			_ = l.Unlock()
		}
		t.Fatalf("got %v, want the wait stopped", err)
	}

	// The holder lets go when Wait says it waits.
	busy := 0
	l, err := Wait(t.Context(), path, func() {
		busy++
		if err := held.Unlock(); err != nil {
			t.Error(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if busy != 1 {
		t.Errorf("busy called %d times, want once", busy)
	}
	if err := l.Unlock(); err != nil {
		t.Fatal(err)
	}
}
