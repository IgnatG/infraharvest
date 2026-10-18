// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// driftOutput writes an output directory with a root at each of rels.
func driftOutput(t *testing.T, rels ...string) string {
	t.Helper()
	out := t.TempDir()
	for _, rel := range rels {
		path := filepath.Join(out, filepath.FromSlash(rel), engine.ProvidersFileName)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// fakeDriftCheck returns, by the root's path in out, what results says,
// and records the roots it checked and where it staged them.
type fakeDriftCheck struct {
	out     string
	results map[string]*engine.RootDrift
	errs    map[string]error
	mu      sync.Mutex
	checked []string
	staged  []string
}

func (f *fakeDriftCheck) check(_ context.Context, dir, staging string, _ engine.DriftOptions) (*engine.RootDrift, error) {
	rel := relativePath(f.out, dir)
	f.mu.Lock()
	f.checked = append(f.checked, rel)
	f.staged = append(f.staged, staging)
	f.mu.Unlock()
	if err := f.errs[rel]; err != nil {
		return nil, err
	}
	if r := f.results[rel]; r != nil {
		return r, nil
	}
	return &engine.RootDrift{Backend: "s3", Resources: 1}, nil
}

func driftOptions(out string, f *fakeDriftCheck) DriftOptions {
	return DriftOptions{Out: out, Engine: EngineTerraform, Format: FormatText, Parallel: 2, check: f.check}
}

var reverting = &engine.RootDrift{Backend: "s3", Resources: 2, Changes: []engine.DriftChange{
	{Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "tags.Env", Before: "prod", After: "dev", Kind: engine.DriftChanged, Outcome: engine.DriftReverts, Source: &engine.DriftSource{File: "main.tf", Line: 3}},
	{Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "arn", Before: "a", After: "b", Kind: engine.DriftChanged, Outcome: engine.DriftAbsorbed},
}}

func TestDriftReportsEachRoot(t *testing.T) {
	t.Parallel()
	out := driftOutput(t, "aws/1/eu-west-1", "aws/1/eu-west-2", "aws/1/global")
	f := &fakeDriftCheck{out: out,
		results: map[string]*engine.RootDrift{"aws/1/eu-west-2": reverting},
		errs:    map[string]error{"aws/1/global": &engine.UnsupportedBackendError{Backend: "remote"}},
	}
	var stdout bytes.Buffer
	err := Drift(context.Background(), driftOptions(out, f), &stdout)
	if code := ExitCode(err); code != report.ExitCouldNotRun || !strings.Contains(err.Error(), "aws/1/global") {
		t.Errorf("exit code %d (%v), want %d naming the root", code, err, report.ExitCouldNotRun)
	}
	sort.Strings(f.checked)
	if want := []string{"aws/1/eu-west-1", "aws/1/eu-west-2", "aws/1/global"}; !reflect.DeepEqual(f.checked, want) {
		t.Errorf("checked %v, want %v", f.checked, want)
	}
	for _, s := range f.staged {
		if filepath.Dir(s) != filepath.Join(out, CheckpointDir, "drift") {
			t.Errorf("staged in %s, not in the checkpoints", s)
		}
	}
	if f.staged[0] == f.staged[1] {
		t.Error("two roots staged in the same directory")
	}
	wantOut := "aws/1/eu-west-1: no drift\n" +
		"aws/1/eu-west-2: 1 reverted by the next apply, 1 absorbed\n" +
		"aws/1/global: not checked: the \"remote\" backend isn't supported, as drift plans locally against the state it pulls: drift supports the s3, gcs and azurerm backends and local state\n" +
		"report: " + filepath.Join(out, report.Dir, report.DriftMarkdownFile) + "\n"
	if stdout.String() != wantOut {
		t.Errorf("stdout:\n%s\nwant:\n%s", stdout.String(), wantOut)
	}
	d := readDrift(t, out)
	if d.Totals.Roots != 3 || d.Totals.Failed != 1 || d.Totals.Reverts != 1 || !d.DriftFound {
		t.Errorf("totals %+v", d.Totals)
	}
	c := d.Changes[1]
	if c.Root != "aws/1/eu-west-2" || c.Decision != report.DriftUndecided || c.Source == nil || c.Source.Line != 3 {
		t.Errorf("change %+v", c)
	}
	if _, err := os.Stat(filepath.Join(out, report.Dir, report.DriftMarkdownFile)); err != nil {
		t.Error(err)
	}
}

func readDrift(t *testing.T, out string) report.Drift {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(out, report.Dir, report.DriftJSONFile))
	if err != nil {
		t.Fatal(err)
	}
	var d report.Drift
	if err := json.Unmarshal(content, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDriftExitCodes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		result   *engine.RootDrift
		detailed bool
		want     int
	}{
		{"drift", reverting, false, report.ExitOK},
		{"drift, detailed", reverting, true, report.ExitDrift},
		{"none, detailed", &engine.RootDrift{Resources: 1}, true, report.ExitOK},
		{"absorbed only, detailed", &engine.RootDrift{Resources: 1, Changes: reverting.Changes[1:]}, true, report.ExitOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := driftOutput(t, "aws/1/eu-west-2")
			f := &fakeDriftCheck{out: out, results: map[string]*engine.RootDrift{"aws/1/eu-west-2": tc.result}}
			options := driftOptions(out, f)
			options.DetailedExitCode = tc.detailed
			err := Drift(context.Background(), options, &bytes.Buffer{})
			if got := ExitCode(err); got != tc.want {
				t.Errorf("exit code %d (%v), want %d", got, err, tc.want)
			}
		})
	}
}

func TestDriftJSONOnStdout(t *testing.T) {
	t.Parallel()
	out := driftOutput(t, "aws/1/eu-west-2")
	f := &fakeDriftCheck{out: out, results: map[string]*engine.RootDrift{"aws/1/eu-west-2": reverting}}
	options := driftOptions(out, f)
	options.Format = FormatJSON
	var stdout bytes.Buffer
	if err := Drift(context.Background(), options, &stdout); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(out, report.Dir, report.DriftJSONFile))
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != string(content) {
		t.Errorf("stdout:\n%s\ndrift.json:\n%s", stdout.String(), content)
	}
}

func TestDriftRootPatterns(t *testing.T) {
	t.Parallel()
	out := driftOutput(t, "aws/1/eu-west-1", "aws/1/eu-west-2", "aws/2/eu-west-2")
	f := &fakeDriftCheck{out: out}
	options := driftOptions(out, f)
	options.Roots = []string{"aws/*/eu-west-2"}
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(f.checked)
	if want := []string{"aws/1/eu-west-2", "aws/2/eu-west-2"}; !reflect.DeepEqual(f.checked, want) {
		t.Errorf("checked %v, want %v", f.checked, want)
	}

	options.Roots = []string{"google"}
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "matches --root") {
		t.Errorf("got %v, want no root matching", err)
	}
}

func TestMatchesRoot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		patterns []string
		rel      string
		want     bool
	}{
		{nil, "aws/1/eu-west-2", true},
		{[]string{"aws/1"}, "aws/1/eu-west-2", true},
		{[]string{"aws/1/"}, "aws/1/eu-west-2", true},
		{[]string{"aws/*/eu-*"}, "aws/1/eu-west-2", true},
		{[]string{"aws/*/us-*"}, "aws/1/eu-west-2", false},
		{[]string{"aws/1/eu"}, "aws/1/eu-west-2", false},
		{[]string{"google", "aws"}, "aws/1/eu-west-2", true},
	} {
		if got := matchesRoot(tc.patterns, tc.rel); got != tc.want {
			t.Errorf("matchesRoot(%v, %q) = %v, want %v", tc.patterns, tc.rel, got, tc.want)
		}
	}
}

func TestDriftOptionsChecked(t *testing.T) {
	t.Parallel()
	out := driftOutput(t, "aws/1/eu-west-2")
	for name, change := range map[string]func(*DriftOptions){
		"--engine":         func(o *DriftOptions) { o.Engine = "pulumi" },
		"--format":         func(o *DriftOptions) { o.Format = "yaml" },
		"--parallel":       func(o *DriftOptions) { o.Parallel = 0 },
		"--engine-timeout": func(o *DriftOptions) { o.EngineTimeout = -1 },
		"--root":           func(o *DriftOptions) { o.Roots = []string{"aws/["} },
	} {
		f := &fakeDriftCheck{out: out}
		options := driftOptions(out, f)
		change(&options)
		err := Drift(context.Background(), options, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), name) || ExitCode(err) != report.ExitCouldNotRun {
			t.Errorf("%s: got %v", name, err)
		}
		if len(f.checked) > 0 {
			t.Errorf("%s: checked roots anyway", name)
		}
	}
	err := Drift(context.Background(), driftOptions(t.TempDir(), &fakeDriftCheck{}), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "no generated roots") {
		t.Errorf("got %v, want no roots", err)
	}
}
