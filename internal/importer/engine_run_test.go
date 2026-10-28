// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

func TestEngineRunCombinesImports(t *testing.T) {
	out := t.TempDir()
	options := Options{Out: out, AllowPartial: true}
	run := NewRun()
	err := run.Do(func() error {
		for _, dir := range []string{"global", "us-east-1"} {
			run.used = true
			run.options = options
			run.discovered["aws_vpc"]++
			result := &engine.Result{Imported: []rootfile.Import{{Type: "aws_vpc", Name: dir, ID: dir}}}
			if dir == "us-east-1" {
				result.Rejected = []engine.Rejection{{Address: "aws_vpc.extra", ID: "vpc-2", Errors: []string{"invalid"}}}
				run.discovered["aws_vpc"]++
			}
			run.addDirectory(filepath.Join(out, dir), nil, result, nil)
		}
		return nil
	})

	if code := ExitCode(err); code != report.ExitPartial {
		t.Errorf("exit code %d, want %d (%v)", code, report.ExitPartial, err)
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

// A run that fails before it imports anything replaces an earlier run's
// report with one that records the failure; one that doesn't fail writes
// none.
func TestEngineRunUnused(t *testing.T) {
	out := t.TempDir()
	want := errors.New("no credentials")
	if err := os.MkdirAll(filepath.Join(out, report.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	earlier := []byte(`{"exit_code": 0}`)
	if err := os.WriteFile(filepath.Join(out, report.Dir, "coverage.json"), earlier, 0o644); err != nil {
		t.Fatal(err)
	}

	run := NewRun()
	err := run.Do(func() error {
		run.options = Options{Out: out}
		return want
	})

	if !errors.Is(err, want) {
		t.Errorf("got %v, want %v", err, want)
	}
	var coverage report.Report
	content, readErr := os.ReadFile(filepath.Join(out, report.Dir, "coverage.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err := json.Unmarshal(content, &coverage); err != nil {
		t.Fatal(err)
	}
	if coverage.ExitCode != ExitCode(want) || len(coverage.Failures) != 1 || coverage.Failures[0] != want.Error() {
		t.Errorf("coverage.json: exit code %d, failures %q; want %d and %q", coverage.ExitCode, coverage.Failures, ExitCode(want), want)
	}

	ok := filepath.Join(out, "ok")
	run = NewRun()
	if err := run.Do(func() error {
		run.options = Options{Out: ok}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(ok, report.Dir)); statErr == nil {
		t.Error("report written though nothing was imported or failed")
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

// A run that ends on an error, such as an interrupt, says so in its
// report, with the exit code the process exits with.
func TestEngineRunReportsTheErrorItEndsOn(t *testing.T) {
	out := t.TempDir()
	run := NewRun()
	run.used = true
	run.options = Options{Out: out}

	err := run.finish(context.Canceled)

	if code := ExitCode(err); code != report.ExitCouldNotRun {
		t.Fatalf("exit code %d, want %d (%v)", code, report.ExitCouldNotRun, err)
	}
	var coverage report.Report
	content, readErr := os.ReadFile(filepath.Join(out, report.Dir, "coverage.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err := json.Unmarshal(content, &coverage); err != nil {
		t.Fatal(err)
	}
	if coverage.ExitCode != report.ExitCouldNotRun || len(coverage.Failures) != 1 {
		t.Errorf("coverage.json: exit code %d, failures %q; want %d and the error", coverage.ExitCode, coverage.Failures, report.ExitCouldNotRun)
	}
}

// A child run's listing and what it hands the merge phase join its
// parent's, beside what the parent had.
func TestRunMergesChildState(t *testing.T) {
	parent := NewRun()
	parent.listed = []selection.Resource{{Type: "aws_vpc", ID: "vpc-1"}}
	parent.covered[coverKey("aws/1/eu-west-2", "vpc")] = true
	parent.members = []mergeMember{{env: "dev"}}

	child := parent.child()
	child.used = true
	child.listed = []selection.Resource{{Type: "aws_s3_bucket", ID: "logs"}}
	child.covered[coverKey("aws/2/eu-west-2", "s3")] = true
	child.filtered = true
	child.members = []mergeMember{{env: "prod"}}
	child.vacating = []*handovers{{}}
	parent.merge(child)

	if len(parent.listed) != 2 || len(parent.covered) != 2 || !parent.filtered {
		t.Errorf("discover state: %+v", parent.discoverState)
	}
	if len(parent.members) != 2 || parent.members[1].env != "prod" || len(parent.vacating) != 1 {
		t.Errorf("merge state: %+v", parent.mergeState)
	}
}
