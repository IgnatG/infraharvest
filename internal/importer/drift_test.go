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
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/adapters"
	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/driftpolicy"
	"github.com/IgnatG/infraharvest/internal/engine/drift"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/IgnatG/infraharvest/internal/report"
)

// driftOutput writes an output directory with a root at each of rels.
func driftOutput(t *testing.T, rels ...string) string {
	t.Helper()
	out := t.TempDir()
	for _, rel := range rels {
		path := filepath.Join(out, filepath.FromSlash(rel), rootfile.ProvidersFileName)
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
	results map[string]*drift.Result
	errs    map[string]error
	mu      sync.Mutex
	checked []string
	staged  []string
	// accepted is the outcome of each change a rule accepts, as drift.Check
	// would give it.
	accepted string
}

func (f *fakeDriftCheck) check(_ context.Context, dir, staging string, opts drift.Options) (*drift.Result, error) {
	rel := relativePath(f.out, dir)
	f.mu.Lock()
	f.checked = append(f.checked, rel)
	f.staged = append(f.staged, staging)
	f.mu.Unlock()
	if err := f.errs[rel]; err != nil {
		return nil, err
	}
	if r := f.results[rel]; r != nil {
		if opts.Accept == nil {
			return r, nil
		}
		decided := *r
		decided.Changes = slices.Clone(r.Changes)
		for i, c := range decided.Changes {
			if c.Outcome == drift.Kept && opts.Accept(c) {
				decided.Changes[i].Decision, decided.Changes[i].Outcome = drift.Accept, f.accepted
			}
		}
		return &decided, nil
	}
	return &drift.Result{Backend: "s3", Resources: 1}, nil
}

func driftOptions(out string, f *fakeDriftCheck) DriftOptions {
	return DriftOptions{Out: out, Engine: EngineTerraform, Format: FormatText, Parallel: 2, check: f.check}
}

var reverting = &drift.Result{Backend: "s3", Resources: 2, Changes: []drift.Change{
	{Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "tags.Env", Before: "prod", After: "dev", Kind: drift.Changed, Outcome: drift.Kept, Source: &drift.Source{File: "main.tf", Line: 3}},
	{Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "arn", Before: "a", After: "b", Kind: drift.Changed, Outcome: drift.Absorbed},
}}

func TestDriftReportsEachRoot(t *testing.T) {
	t.Parallel()
	out := driftOutput(t, "aws/1/eu-west-1", "aws/1/eu-west-2", "aws/1/global")
	f := &fakeDriftCheck{out: out,
		results: map[string]*drift.Result{"aws/1/eu-west-2": reverting},
		errs:    map[string]error{"aws/1/global": &drift.UnsupportedBackendError{Backend: "remote", Supported: []string{"s3", "gcs", "azurerm"}}},
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
	if d.Totals.Roots != 3 || d.Totals.Failed != 1 || d.Totals.Kept != 1 || !d.DriftFound {
		t.Errorf("totals %+v", d.Totals)
	}
	c := d.Changes[1]
	if c.Root != "aws/1/eu-west-2" || c.Decision != report.DriftKeep || c.Source == nil || c.Source.Line != 3 {
		t.Errorf("change %+v", c)
	}
	if _, err := os.Stat(filepath.Join(out, report.Dir, report.DriftMarkdownFile)); err != nil {
		t.Error(err)
	}
}

// A relative out, such as the Action's default, is staged at an
// absolute path: Terraform, running in the root, can't resolve one
// relative to where drift runs.
func TestDriftStagesRelativeOutAbsolute(t *testing.T) {
	out := driftOutput(t, "aws/1/eu-west-1")
	t.Chdir(filepath.Dir(out))
	// As drift sees it, where the temporary directory is behind a link.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDriftCheck{out: filepath.Join(wd, filepath.Base(out))}
	if err := Drift(context.Background(), driftOptions(filepath.Base(out), f), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.checked, []string{"aws/1/eu-west-1"}) {
		t.Errorf("checked %v", f.checked)
	}
	for _, s := range f.staged {
		if !filepath.IsAbs(s) {
			t.Errorf("staged in %s, a relative path", s)
		}
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
		result   *drift.Result
		detailed bool
		want     int
	}{
		{"drift", reverting, false, report.ExitOK},
		{"drift, detailed", reverting, true, report.ExitDrift},
		{"none, detailed", &drift.Result{Resources: 1}, true, report.ExitOK},
		{"absorbed only, detailed", &drift.Result{Resources: 1, Changes: reverting.Changes[1:]}, true, report.ExitOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := driftOutput(t, "aws/1/eu-west-2")
			f := &fakeDriftCheck{out: out, results: map[string]*drift.Result{"aws/1/eu-west-2": tc.result}}
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
	f := &fakeDriftCheck{out: out, results: map[string]*drift.Result{"aws/1/eu-west-2": reverting}}
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

func TestDriftAcceptsByRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		accepted string // what accepting a change makes of it
		rules    []driftpolicy.Rule
		want     int
	}{
		{"none accepted", report.DriftWritten, nil, report.ExitDrift},
		{"all written", report.DriftWritten, []driftpolicy.Rule{{Accept: true}}, report.ExitOK},
		{"rejected", report.DriftRejectedByPlan, []driftpolicy.Rule{{Accept: true}}, report.ExitDriftRejected},
		{"rejected by a check", report.DriftRejectedByCheck, []driftpolicy.Rule{{Accept: true}}, report.ExitDriftRejected},
		{"a hand edit", report.DriftManual, []driftpolicy.Rule{{Accept: true}}, report.ExitDrift},
		{"in another root", report.DriftWritten, []driftpolicy.Rule{{Accept: true, Field: "root", Pattern: "google/*"}}, report.ExitDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := driftOutput(t, "aws/1/eu-west-2")
			f := &fakeDriftCheck{out: out, results: map[string]*drift.Result{"aws/1/eu-west-2": reverting}, accepted: tc.accepted}
			options := driftOptions(out, f)
			options.Rules = tc.rules
			options.DetailedExitCode = true
			err := Drift(context.Background(), options, &bytes.Buffer{})
			if got := ExitCode(err); got != tc.want {
				t.Errorf("exit code %d (%v), want %d", got, err, tc.want)
			}
			d := readDrift(t, out)
			c := d.Changes[1]
			if c.Attribute != "tags.Env" {
				t.Fatalf("change %+v", c)
			}
			wantDecision, wantOutcome := report.DriftKeep, report.DriftKept
			if len(tc.rules) > 0 && tc.rules[0].Field == "" {
				wantDecision, wantOutcome = report.DriftAccept, tc.accepted
			}
			if c.Decision != wantDecision || c.Outcome != wantOutcome {
				t.Errorf("tags.Env: %s, %s, want %s, %s", c.Decision, c.Outcome, wantDecision, wantOutcome)
			}
		})
	}

	out := driftOutput(t, "aws/1/eu-west-2")
	options := driftOptions(out, &fakeDriftCheck{out: out})
	options.Rules = []driftpolicy.Rule{{Accept: true, Field: "name", Pattern: "x"}}
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--accept") {
		t.Errorf("got %v, want the rule refused", err)
	}
}

// TestDriftChecksEachEnvironment checks that a root environments share is
// checked once per environment, as --environment names them, or every one
// with --environment all, and that a change of each goes into the report
// and the decisions file as that environment's. Without --environment,
// the run fails, as the credentials it runs with are one environment's.
func TestDriftChecksEachEnvironment(t *testing.T) {
	t.Parallel()
	out := driftOutput(t, "aws/us-east-1", "aws/1/global")
	for _, env := range []string{"dev", "prod"} {
		path := filepath.Join(out, "aws", "us-east-1", "vars", env, "terraform.tfvars")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("env = \""+env+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var checked, staged []string
	// The checks that initialise the root's directory holding a lock, and
	// how many hold it at once, by root.
	locked := map[string]bool{}
	inside := map[string]int{}
	var overlapped []string
	check := func(_ context.Context, dir, staging string, opts drift.Options) (*drift.Result, error) {
		name := report.DriftRoot{Path: relativePath(out, dir), Environment: opts.Environment}.Name()
		mu.Lock()
		checked = append(checked, name)
		staged = append(staged, staging)
		locked[name] = opts.WithRootLock != nil
		mu.Unlock()
		if opts.WithRootLock != nil {
			if err := opts.WithRootLock(func() error {
				mu.Lock()
				if inside[dir]++; inside[dir] > 1 {
					overlapped = append(overlapped, name)
				}
				mu.Unlock()
				time.Sleep(50 * time.Millisecond)
				mu.Lock()
				inside[dir]--
				mu.Unlock()
				return nil
			}); err != nil {
				return nil, err
			}
		}
		// Each environment is checked with its var files.
		if _, err := os.Stat(filepath.Join(dir, "vars", opts.Environment, "terraform.tfvars")); opts.Environment != "" && err != nil {
			return nil, err
		}
		if opts.Environment == "" {
			return &drift.Result{Backend: "s3", Resources: 1}, nil
		}
		return reverting, nil
	}
	run := func(envs ...string) ([]string, error) {
		checked, staged = nil, nil
		options := DriftOptions{Out: out, Engine: EngineTerraform, Format: FormatText, Parallel: 2, Environments: envs, WriteDecisions: true, check: check}
		var stdout bytes.Buffer
		err := Drift(context.Background(), options, &stdout)
		sort.Strings(checked)
		return checked, err
	}

	if _, err := run(); err == nil || !strings.Contains(err.Error(), "dev, prod") || !strings.Contains(err.Error(), "--environment") {
		t.Fatalf("no --environment: %v, want an error naming the environments and the flag", err)
	}
	if _, err := run("all", "dev"); err == nil || !strings.Contains(err.Error(), "no other") {
		t.Errorf("--environment all,dev: %v, want an error", err)
	}
	got, err := run("all")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"aws/1/global", "aws/us-east-1 (dev)", "aws/us-east-1 (prod)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("checked %v, want %v", got, want)
	}
	if len(slices.Compact(slices.Sorted(slices.Values(staged)))) != 3 {
		t.Errorf("two checks staged in the same directory: %v", staged)
	}
	// The environments of the shared root initialise its directory one at
	// a time; the other root locks nothing.
	if !locked["aws/us-east-1 (dev)"] || !locked["aws/us-east-1 (prod)"] || len(overlapped) > 0 {
		t.Errorf("the environments of aws/us-east-1: locked %v, at once %v; want them to share one lock", locked, overlapped)
	}
	if locked["aws/1/global"] {
		t.Errorf("aws/1/global holds a lock")
	}
	d := readDrift(t, out)
	var envs []string
	for _, c := range d.Changes {
		if c.Attribute == "tags.Env" {
			envs = append(envs, c.Name())
		}
	}
	if want := []string{"aws/us-east-1 (dev)", "aws/us-east-1 (prod)"}; !reflect.DeepEqual(envs, want) {
		t.Errorf("changes of tags.Env: %v, want %v", envs, want)
	}
	f, err := driftpolicy.LoadDecisions(filepath.Join(out, driftpolicy.DefaultDecisionsFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Changes) != 2 || f.Changes[0].Environment != "dev" || f.Changes[1].Environment != "prod" {
		t.Errorf("decisions: %+v", f.Changes)
	}

	if got, err = run("prod"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"aws/1/global", "aws/us-east-1 (prod)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("--environment prod: checked %v, want %v", got, want)
	}
	if _, err := run("qa"); err == nil || !strings.Contains(err.Error(), "qa") {
		t.Errorf("--environment qa: %v, want an error naming it", err)
	}
}

// The --catalogue overlay applies to the checks, as to the import: its
// state-only arguments aren't drift. One that doesn't load fails the run
// before any root is checked.
func TestDriftUsesTheCatalogueOverlay(t *testing.T) {
	t.Parallel()
	out := driftOutput(t, "aws/1/eu-west-2")
	dir := t.TempDir()
	overlay := "provider: aws\ntypes:\n  aws_s3_bucket:\n    state_only: [widget_setting]\n"
	if err := os.WriteFile(filepath.Join(dir, "aws.yaml"), []byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var stateOnly []map[string][]string
	check := func(_ context.Context, _, _ string, opts drift.Options) (*drift.Result, error) {
		mu.Lock()
		stateOnly = append(stateOnly, opts.StateOnly)
		mu.Unlock()
		return &drift.Result{Backend: "s3", Resources: 1}, nil
	}
	options := DriftOptions{
		Out: out, Engine: EngineTerraform, Format: FormatText, Parallel: 1, Catalogue: dir,
		Provider: func(name string) harvest.Provider {
			if name == "aws" {
				return &catalogueProvider{}
			}
			return nil
		},
		Names: []string{"aws"},
		check: check,
	}
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(stateOnly) != 1 || !slices.Contains(stateOnly[0]["aws_s3_bucket"], "widget_setting") {
		t.Errorf("state-only arguments: %v, want the overlay's widget_setting of aws_s3_bucket", stateOnly)
	}

	stateOnly = nil
	options.Catalogue = filepath.Join(dir, "missing")
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--catalogue") {
		t.Errorf("a catalogue that doesn't load: got %v, want a --catalogue error", err)
	}
	if len(stateOnly) != 0 {
		t.Error("checked roots with a catalogue that doesn't load")
	}
}

// TestDriftTargetsNeedAnEnvironmentForSeveral checks that roots of one
// environment need no --environment, and that roots of several need it
// even when each root has one of them, as one run's credentials are one
// environment's.
func TestDriftTargetsNeedAnEnvironmentForSeveral(t *testing.T) {
	t.Parallel()
	root := func(envs ...string) string {
		dir := t.TempDir()
		for _, env := range envs {
			path := filepath.Join(dir, "vars", env, "terraform.tfvars")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("env = \""+env+"\"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	plain, dev, prod := root(), root("dev"), root("prod")
	targets, err := driftTargets([]string{plain, dev}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[1].env != "dev" {
		t.Errorf("one environment: %+v", targets)
	}
	if _, err := driftTargets([]string{dev, prod}, nil); err == nil || !strings.Contains(err.Error(), "dev, prod") {
		t.Errorf("roots of dev and prod: %v, want an error naming them", err)
	}
	if targets, err := driftTargets([]string{dev, prod}, []string{"all"}); err != nil || len(targets) != 2 {
		t.Errorf("--environment all: %+v, %v", targets, err)
	}
}

// The checks of an accepted change's edited code leave the
// organisation's own modules in directories to their owners, as the
// import's do: each root gets the adapters of its provider's, from the
// configuration file's modules, then those of its provider's curated
// modules, which trace changes into their calls. Modules drift would have
// to fetch aren't.
func TestDriftPassesTheRootsModules(t *testing.T) {
	t.Parallel()
	out := driftOutput(t, "aws/1/eu-west-2")
	module := func(content string) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	bucket := module("variable \"name\" {\n  type = string\n}\n\nresource \"aws_s3_bucket\" \"this\" {\n  bucket = var.name\n}\n")
	storage := module("resource \"google_storage_bucket\" \"this\" {\n  name     = \"b\"\n  location = \"EU\"\n}\n")
	var mu sync.Mutex
	var got [][]adapters.Adapter
	check := func(_ context.Context, _, _ string, opts drift.Options) (*drift.Result, error) {
		mu.Lock()
		got = append(got, opts.Adapters)
		mu.Unlock()
		return &drift.Result{Backend: "s3", Resources: 1}, nil
	}
	options := DriftOptions{
		Out: out, Engine: EngineTerraform, Format: FormatText, Parallel: 1,
		Provider: func(name string) harvest.Provider {
			if name == "aws" {
				return &curatedProvider{}
			}
			return nil
		},
		Names:   []string{"aws"},
		Modules: []config.Module{{Source: bucket}, {Source: storage}, {Source: "git::https://example.com/modules.git//queue?ref=v1"}},
		check:   check,
	}
	if err := Drift(t.Context(), options, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	curated := adapters.For("aws")
	if len(got) != 1 || len(got[0]) != 1+len(curated) || got[0][0].Source != bucket {
		t.Fatalf("adapters %+v, want the bucket module's, then the curated ones", got)
	}
	for i, a := range curated {
		if got[0][1+i].Source != a.Source || got[0][1+i].Version != a.Version {
			t.Errorf("adapter %d: %s %s, want %s %s", 1+i, got[0][1+i].Source, got[0][1+i].Version, a.Source, a.Version)
		}
	}
}

// curatedProvider is a catalogueProvider whose profile names the curated
// modules of aws.
type curatedProvider struct{ catalogueProvider }

func (*curatedProvider) Profile() harvest.Profile { return harvest.Profile{Modules: "aws"} }

// Terraform runs on roots that may come from someone else with a CLI
// configuration of the run's own, which installs providers from their
// registries alone: no filesystem mirror, such as a terraform.d directory
// the roots carry.
func TestUntrustedTerraform(t *testing.T) {
	private := t.TempDir()

	env, err := untrustedTerraform(private)

	if err != nil {
		t.Fatal(err)
	}
	config := env["TF_CLI_CONFIG_FILE"]
	content, err := os.ReadFile(config)
	if err != nil || filepath.Dir(config) != private || string(content) != "provider_installation {\n  direct {}\n}\n" {
		t.Errorf("TF_CLI_CONFIG_FILE %s has %q (%v), want direct installation alone, in %s", config, content, err, private)
	}
}

// With RootsDigest, drift checks no root unless the roots still have it
// once the output directory is locked.
func TestDriftChecksTheRootsDigest(t *testing.T) {
	out := driftOutput(t, "aws/1/eu-west-2")
	digest, err := driftpolicy.RootsDigest(out)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDriftCheck{out: out}
	options := driftOptions(out, f)
	options.RootsDigest = digest
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err != nil || len(f.checked) != 1 {
		t.Fatalf("the roots checked: %v, checked %v", err, f.checked)
	}

	if err := os.WriteFile(filepath.Join(out, "aws", "1", "eu-west-2", "x.tf"), []byte("data \"external\" \"x\" {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f = &fakeDriftCheck{out: out}
	options = driftOptions(out, f)
	options.RootsDigest = digest
	err = Drift(context.Background(), options, &bytes.Buffer{})
	if ExitCode(err) != report.ExitCouldNotRun || err == nil || !strings.Contains(err.Error(), "changed since they were checked") {
		t.Errorf("got %v, want the roots refused", err)
	}
	if len(f.checked) != 0 {
		t.Errorf("checked %v, roots that changed", f.checked)
	}
}

// With RootsDigest, Terraform runs on the roots as the user confirmed
// them: a root rewritten while another runs is checked as it was, from a
// copy outside the output directory, and what a check writes into its
// root, such as the lock file its init writes, reaches the output
// directory.
func TestDriftRunsTheConfirmedRoots(t *testing.T) {
	rels := []string{"aws/1/eu-west-1", "aws/1/eu-west-2"}
	out := driftOutput(t, rels...)
	mainFile := func(dir string) string { return filepath.Join(dir, "main.tf") }
	confirmed := "resource \"aws_s3_bucket\" \"logs\" {}\n"
	rewritten := "data \"external\" \"x\" {\n  program = [\"sh\", \"-c\", \"id\"]\n}\n"
	for _, rel := range rels {
		if err := os.WriteFile(mainFile(filepath.Join(out, filepath.FromSlash(rel))), []byte(confirmed), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Local state, which the digest doesn't cover, is copied for drift to
	// pull.
	state := filepath.Join(out, "aws", "1", "eu-west-1", "terraform.tfstate")
	if err := os.WriteFile(state, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := driftpolicy.RootsDigest(out)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]string{}
	options := DriftOptions{Out: out, Engine: EngineTerraform, Format: FormatText, Parallel: 1, RootsDigest: digest}
	options.check = func(_ context.Context, dir, staging string, _ drift.Options) (*drift.Result, error) {
		for _, path := range []string{dir, staging} {
			if strings.HasPrefix(path, out+string(filepath.Separator)) {
				t.Errorf("Terraform runs in %s, in the output directory", path)
			}
		}
		// Whichever root runs first, the others are rewritten before
		// they run.
		for _, rel := range rels {
			if err := os.WriteFile(mainFile(filepath.Join(out, filepath.FromSlash(rel))), []byte(rewritten), 0o644); err != nil {
				return nil, err
			}
		}
		content, err := os.ReadFile(mainFile(dir))
		if err != nil {
			return nil, err
		}
		mu.Lock()
		seen[filepath.Base(dir)] = string(content)
		mu.Unlock()
		if _, err := os.Stat(filepath.Join(dir, "terraform.tfstate")); (err == nil) != (filepath.Base(dir) == "eu-west-1") {
			t.Errorf("%s: the state copied: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".terraform.lock.hcl"), []byte("# "+filepath.Base(dir)+"\n"), 0o644); err != nil {
			return nil, err
		}
		return &drift.Result{Backend: "s3", Resources: 1}, nil
	}

	if err := Drift(context.Background(), options, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	if want := map[string]string{"eu-west-1": confirmed, "eu-west-2": confirmed}; !reflect.DeepEqual(seen, want) {
		t.Errorf("the roots were checked with %q, want them as confirmed", seen)
	}
	for _, rel := range rels {
		dir := filepath.Join(out, filepath.FromSlash(rel))
		if lock, err := os.ReadFile(filepath.Join(dir, ".terraform.lock.hcl")); err != nil || string(lock) != "# "+filepath.Base(dir)+"\n" {
			t.Errorf("%s: lock file %q (%v), want the one its check wrote", rel, lock, err)
		}
		// Only what the run wrote is written back.
		if content, err := os.ReadFile(mainFile(dir)); err != nil || string(content) != rewritten {
			t.Errorf("%s: main.tf %q (%v), want it as it was rewritten", rel, content, err)
		}
	}
}
