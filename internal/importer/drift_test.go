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
	// accepted is the outcome of each change a rule accepts, as engine.Drift
	// would give it.
	accepted string
}

func (f *fakeDriftCheck) check(_ context.Context, dir, staging string, opts engine.DriftOptions) (*engine.RootDrift, error) {
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
			if c.Outcome == engine.DriftKept && opts.Accept(c) {
				decided.Changes[i].Decision, decided.Changes[i].Outcome = engine.DriftAccept, f.accepted
			}
		}
		return &decided, nil
	}
	return &engine.RootDrift{Backend: "s3", Resources: 1}, nil
}

func driftOptions(out string, f *fakeDriftCheck) DriftOptions {
	return DriftOptions{Out: out, Engine: EngineTerraform, Format: FormatText, Parallel: 2, check: f.check}
}

var reverting = &engine.RootDrift{Backend: "s3", Resources: 2, Changes: []engine.DriftChange{
	{Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "tags.Env", Before: "prod", After: "dev", Kind: engine.DriftChanged, Outcome: engine.DriftKept, Source: &engine.DriftSource{File: "main.tf", Line: 3}},
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

func TestParseDriftRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		s    string
		want DriftRule
	}{
		{"all", DriftRule{Accept: true}},
		{"attribute=tags.*", DriftRule{Accept: true, Field: "attribute", Pattern: "tags.*"}},
		{"type=aws_security_group", DriftRule{Accept: true, Field: "type", Pattern: "aws_security_group"}},
		{"address=aws_s3_bucket.*", DriftRule{Accept: true, Field: "address", Pattern: "aws_s3_bucket.*"}},
		{"root=aws/*/eu-west-2", DriftRule{Accept: true, Field: "root", Pattern: "aws/*/eu-west-2"}},
		{"attribute=tags.a=b", DriftRule{Accept: true, Field: "attribute", Pattern: "tags.a=b"}},
	} {
		s, want := tc.s, tc.want
		got, err := ParseDriftRule(s, true)
		if err != nil || got != want {
			t.Errorf("%s: got %+v (%v), want %+v", s, got, err, want)
		}
		if got.String() != s {
			t.Errorf("%s: written back as %s", s, got.String())
		}
	}
	for _, s := range []string{"", "everything", "name=x", "attribute=", "=x"} {
		if _, err := ParseDriftRule(s, false); err == nil || !strings.Contains(err.Error(), "--keep") {
			t.Errorf("%q: got %v, want an error naming --keep", s, err)
		}
	}
}

func TestDriftRulesLastMatchWins(t *testing.T) {
	t.Parallel()
	tag := engine.DriftChange{Address: "aws_security_group.web", Type: "aws_security_group", Attribute: "tags.Env"}
	rule := engine.DriftChange{Address: "aws_security_group.web", Type: "aws_security_group", Attribute: "ingress[0].cidr_blocks"}
	bucket := engine.DriftChange{Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "versioning[0].enabled"}
	parse := func(accept bool, s string) DriftRule {
		r, err := ParseDriftRule(s, accept)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	rules := []DriftRule{parse(true, "all"), parse(false, "type=aws_security_group"), parse(true, "attribute=tags.*")}
	for _, tc := range []struct {
		rules []DriftRule
		root  string
		c     engine.DriftChange
		want  bool
	}{
		{nil, "aws/1/eu-west-2", tag, false},
		{rules, "aws/1/eu-west-2", tag, true},
		{rules, "aws/1/eu-west-2", rule, false},
		{rules, "aws/1/eu-west-2", bucket, true},
		{[]DriftRule{parse(true, "root=aws/*/eu-west-1")}, "aws/1/eu-west-2", bucket, false},
		{[]DriftRule{parse(true, "root=aws/*/eu-west-1")}, "aws/1/eu-west-1", bucket, true},
		{[]DriftRule{parse(true, "address=aws_s3_bucket.*"), parse(false, "all")}, "aws/1/eu-west-2", bucket, false},
	} {
		if got := (driftDecider{rules: tc.rules}).decide(tc.root, tc.c).accept; got != tc.want {
			t.Errorf("%v in %s, %s %s: accepted %v, want %v", tc.rules, tc.root, tc.c.Address, tc.c.Attribute, got, tc.want)
		}
	}
}

func TestDriftAcceptsByRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		accepted string // what accepting a change makes of it
		rules    []DriftRule
		want     int
	}{
		{"none accepted", report.DriftWritten, nil, report.ExitDrift},
		{"all written", report.DriftWritten, []DriftRule{{Accept: true}}, report.ExitOK},
		{"rejected", report.DriftRejectedByPlan, []DriftRule{{Accept: true}}, report.ExitDriftRejected},
		{"a hand edit", report.DriftManual, []DriftRule{{Accept: true}}, report.ExitDrift},
		{"in another root", report.DriftWritten, []DriftRule{{Accept: true, Field: "root", Pattern: "google/*"}}, report.ExitDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := driftOutput(t, "aws/1/eu-west-2")
			f := &fakeDriftCheck{out: out, results: map[string]*engine.RootDrift{"aws/1/eu-west-2": reverting}, accepted: tc.accepted}
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
	options.Rules = []DriftRule{{Accept: true, Field: "name", Pattern: "x"}}
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--accept") {
		t.Errorf("got %v, want the rule refused", err)
	}
}

// A rule on a type or an address matches a change of shared tags if it
// matches one of the resources the change covers, or, for an address,
// where the tags are kept.
func TestDriftRulesMatchSharedTags(t *testing.T) {
	t.Parallel()
	shared := engine.DriftChange{Address: "local.tags", Attribute: "tags.Env", Covers: []string{"aws_s3_bucket.logs", "module.queues.aws_sqs_queue.this[0]"}}
	for _, tc := range []struct {
		rule string
		want bool
	}{
		{"attribute=tags.*", true},
		{"attribute=labels.*", false},
		{"type=aws_s3_bucket", true},
		{"type=aws_sqs_queue", true},
		{"type=aws_security_group", false},
		{"address=local.tags", true},
		{"address=aws_s3_bucket.*", true},
		{"address=aws_instance.*", false},
		{"root=aws/*", true},
	} {
		r, err := ParseDriftRule(tc.rule, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := (driftDecider{rules: []DriftRule{r}}).decide("aws/1/eu-west-2", shared).accept; got != tc.want {
			t.Errorf("%s: accepted %v, want %v", tc.rule, got, tc.want)
		}
	}
}

// A module call's instance key may have dots, which aren't steps of the
// address.
func TestAddressType(t *testing.T) {
	t.Parallel()
	for address, want := range map[string]string{
		"aws_s3_bucket.logs":                                 "aws_s3_bucket",
		"aws_s3_bucket.logs[0]":                              "aws_s3_bucket",
		"data.aws_iam_policy_document.p":                     "aws_iam_policy_document",
		"module.logs.aws_s3_bucket.this[0]":                  "aws_s3_bucket",
		`module.logs["a.b"].aws_s3_bucket.this`:              "aws_s3_bucket",
		`module.x["a.b"].module.y[1].aws_sqs_queue.q["c.d"]`: "aws_sqs_queue",
		`module.x["say \"a.b\" ]"].aws_iam_role.this`:        "aws_iam_role",
	} {
		if got := addressType(address); got != want {
			t.Errorf("%s: got %s, want %s", address, got, want)
		}
	}
}

// TestDriftChecksEachEnvironment checks that a root environments share is
// checked once per environment, as --environment names them, and that a
// change of each goes into the report and the decisions file as that
// environment's.
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
	check := func(_ context.Context, dir, staging string, opts engine.DriftOptions) (*engine.RootDrift, error) {
		mu.Lock()
		checked = append(checked, report.DriftRoot{Path: relativePath(out, dir), Environment: opts.Environment}.Name())
		staged = append(staged, staging)
		mu.Unlock()
		// Each environment is checked with its var files.
		if _, err := os.Stat(filepath.Join(dir, "vars", opts.Environment, "terraform.tfvars")); opts.Environment != "" && err != nil {
			return nil, err
		}
		if opts.Environment == "" {
			return &engine.RootDrift{Backend: "s3", Resources: 1}, nil
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

	got, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"aws/1/global", "aws/us-east-1 (dev)", "aws/us-east-1 (prod)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("checked %v, want %v", got, want)
	}
	if len(slices.Compact(slices.Sorted(slices.Values(staged)))) != 3 {
		t.Errorf("two checks staged in the same directory: %v", staged)
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
	f, err := LoadDriftDecisions(filepath.Join(out, DefaultDriftDecisionsFile))
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
