// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProfiles(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		cpuProfileEnv:           filepath.Join(dir, "cpu.pprof"),
		memProfileEnv:           filepath.Join(dir, "mem.pprof"),
		goroutineLeakProfileEnv: filepath.Join(dir, "leak.pprof"),
	}
	stop, err := startProfiles(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	for _, path := range env {
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			t.Errorf("%s: %v", path, err)
		}
	}

	// Without the variables, nothing is written.
	stop, err = startProfiles(func(string) string { return "" })
	if err != nil || stop() != nil {
		t.Errorf("without profiles: %v", err)
	}
}
