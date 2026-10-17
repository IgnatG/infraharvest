// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/report"
)

// One run at a time holds an output directory's lock: another is refused,
// naming the holder, unless it forces its way in, and takes the lock once
// it is released.
func TestLockOutput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	held, err := lockOutput(out, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := lockOutput(out, false)
	if err == nil {
		_ = second.Unlock()
		t.Fatal("a second lock of the output directory was taken")
	}
	for _, want := range []string{out + " is in use by another infraharvest run", fmt.Sprintf("process %d on ", os.Getpid()), "--force-unlock"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q doesn't say %q", err, want)
		}
	}
	if lock, err := lockOutput(out, true); err != nil || lock != nil {
		t.Errorf("with force: got %v, %v; want to go on without the lock", lock, err)
	}
	if err := held.Unlock(); err != nil {
		t.Fatal(err)
	}
	again, err := lockOutput(out, false)
	if err != nil {
		t.Fatalf("after the unlock: %v", err)
	}
	if err := again.Unlock(); err != nil {
		t.Fatal(err)
	}
}

// A run writes no report into an output directory another run holds, and
// one that finishes releases the lock its Import calls took.
func TestRunLocksTheOutput(t *testing.T) {
	out := t.TempDir()
	holder := NewRun()
	if err := holder.lockOut(Options{Out: out}); err != nil {
		t.Fatal(err)
	}
	// The command's other Import calls share the lock.
	if err := holder.child().lockOut(Options{Out: out}); err != nil {
		t.Errorf("a child run of the holder: %v", err)
	}

	other := NewRun()
	other.used = true
	other.options = Options{Out: out}
	err := other.finish(nil)
	if code := ExitCode(err); code != report.ExitCouldNotRun || !strings.Contains(err.Error(), "is in use by another infraharvest run") {
		t.Errorf("exit code %d, error %v; want %d and the holder named", code, err, report.ExitCouldNotRun)
	}
	if _, statErr := os.Stat(filepath.Join(out, report.Dir)); statErr == nil {
		t.Error("report written into an output directory another run holds")
	}

	if err := holder.finish(nil); err != nil {
		t.Fatal(err)
	}
	lock, err := lockOutput(out, false)
	if err != nil {
		t.Fatalf("after the holder finished: %v", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
}
