// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/IgnatG/infraharvest/engine"
	"github.com/IgnatG/infraharvest/report"
)

// One command, two Import calls (as the AWS command makes for global and
// regional resources): one report covering both, one exit code.
func TestEngineRunCombinesImports(t *testing.T) {
	out := t.TempDir()
	options := ImportOptions{PathOutput: out, AllowPartial: true}
	runE := withEngineRun(func(*cobra.Command, []string) error {
		for _, dir := range []string{"global", "us-east-1"} {
			activeRun.used = true
			activeRun.options = options
			activeRun.discovered["aws_vpc"]++
			result := &engine.Result{Imported: []engine.Import{{Type: "aws_vpc", Name: dir, ID: dir}}}
			if dir == "us-east-1" {
				result.Rejected = []engine.Rejection{{Address: "aws_vpc.extra", ID: "vpc-2", Errors: []string{"invalid"}}}
				activeRun.discovered["aws_vpc"]++
			}
			activeRun.addDirectory(filepath.Join(out, dir), nil, result, nil)
		}
		return nil
	})

	err := runE(nil, nil)

	if code := ExitCode(err); code != report.ExitPartial {
		t.Errorf("exit code %d, want %d (%v)", code, report.ExitPartial, err)
	}
	if activeRun != nil {
		t.Error("run still active after the command")
	}
	var coverage report.Report
	content, readErr := os.ReadFile(filepath.Join(out, report.Dir, "coverage.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err := json.Unmarshal(content, &coverage); err != nil {
		t.Fatal(err)
	}
	if len(coverage.Directories) != 2 || coverage.Totals.Imported != 2 || coverage.Totals.LeftOut != 1 {
		t.Errorf("want both imports in one report, got %+v", coverage)
	}
}

// Legacy imports don't use the run: errors pass through, no report.
func TestEngineRunUnused(t *testing.T) {
	out := t.TempDir()
	want := errors.New("legacy failure")

	err := withEngineRun(func(*cobra.Command, []string) error { return want })(nil, nil)

	if !errors.Is(err, want) {
		t.Errorf("got %v, want %v", err, want)
	}
	if _, statErr := os.Stat(filepath.Join(out, report.Dir)); statErr == nil {
		t.Error("report written for a legacy import")
	}
}
