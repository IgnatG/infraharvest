// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

func TestEngineRunCombinesImports(t *testing.T) {
	out := t.TempDir()
	options := Options{Out: out, AllowPartial: true}
	err := NewRun().Do(func() error {
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

func TestEngineRunUnused(t *testing.T) {
	out := t.TempDir()
	want := errors.New("no credentials")

	err := NewRun().Do(func() error {
		activeRun.options = Options{Out: out}
		return want
	})

	if !errors.Is(err, want) {
		t.Errorf("got %v, want %v", err, want)
	}
	if _, statErr := os.Stat(filepath.Join(out, report.Dir)); statErr == nil {
		t.Error("report written though nothing was imported")
	}
}

func TestEngineRunCouldNotWriteReport(t *testing.T) {
	out := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(out, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	partial := checkFailures([]error{errors.New("service sqs: access denied")}, true)
	for name, options := range map[string]Options{
		"import":   {Out: out, AllowPartial: true},
		"discover": {Discover: true, Selection: filepath.Join(out, "selection.yaml"), AllowPartial: true},
	} {
		run := NewRun()
		run.used = true
		run.options = options

		err := run.finish(partial)

		if code := ExitCode(err); code != report.ExitCouldNotRun {
			t.Errorf("%s: exit code %d, want %d (%v)", name, code, report.ExitCouldNotRun, err)
		}
		if !errors.Is(err, partial) {
			t.Errorf("%s: the import's own error is lost: %v", name, err)
		}
	}
}
