// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Without a terminal, pick fails before it reads the selection file, and
// leaves it alone: the message says to edit the file instead.
func TestPickNeedsATerminal(t *testing.T) {
	if interactive() {
		t.Skip("standard input and output are a terminal")
	}
	path := filepath.Join(t.TempDir(), "selection.yaml")
	if err := os.WriteFile(path, []byte("not a selection file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pick(path); !errors.Is(err, errNoTerminal) {
		t.Errorf("pick: %v, want %v", err, errNoTerminal)
	}

	cmd := newPickCmd()
	cmd.SetArgs([]string{"--selection=" + path})
	cmd.SilenceErrors = true
	if err := cmd.Execute(); !errors.Is(err, errNoTerminal) {
		t.Errorf("pick command: %v, want %v", err, errNoTerminal)
	}
	if got, _ := os.ReadFile(path); string(got) != "not a selection file" {
		t.Errorf("the selection file changed: %q", got)
	}
}
