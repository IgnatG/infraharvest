// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package mcpserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A junction, which Go doesn't report as a symbolic link and
// filepath.EvalSymlinks doesn't follow, is refused as a way out of the
// root: a file named through one, and one under a directory an import
// writes into.
func TestJunctionsRefused(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "credentials"), []byte("[default]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(root, "generated", "aws")
	if err := os.MkdirAll(filepath.Dir(junction), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, outside).CombinedOutput(); err != nil {
		t.Skipf("can't make a junction: %v: %s", err, out)
	}
	tools := newTools(&fakeRunner{}, root)

	if _, _, err := tools.readWithin("selection", filepath.Join(junction, "credentials")); err == nil || !strings.Contains(err.Error(), "junction") {
		t.Errorf("read through a junction: %v, want it refused", err)
	}
	if err := tools.linksStayUnder("out", filepath.Join(root, "generated")); err == nil || !strings.Contains(err.Error(), "junction") {
		t.Errorf("a junction under the output: %v, want it refused", err)
	}
}
