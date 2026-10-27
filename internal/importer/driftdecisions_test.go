// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/config"
	"github.com/IgnatG/infraharvest/internal/driftpolicy"
	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// envFingerprint is the fingerprint of reverting's change of tags.Env.
var envFingerprint = decisionFingerprint("prod", "dev")

// decisionFingerprint is the fingerprint --write-decisions writes for a
// change from before to after.
func decisionFingerprint(before, after any) string {
	f := driftpolicy.NewDecisions()
	f.Merge([]report.DriftChange{{Root: "r", Address: "a", Kind: engine.DriftChanged, Before: before, After: after, Outcome: report.DriftKept}}, nil)
	return f.Changes[0].Fingerprint
}

// writeDecisions writes a decisions file into dir with an entry for
// reverting's change of tags.Env.
func writeDecisions(t *testing.T, dir, fingerprint, decision string) string {
	t.Helper()
	path := filepath.Join(dir, "decisions.yaml")
	content := "version: 1\ndefaults: { decision: keep }\nchanges:\n" +
		"  - { root: aws/1/eu-west-2, address: aws_s3_bucket.logs, attribute: tags.Env, fingerprint: " + fingerprint + ", decision: " + decision + ", note: mine }\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// envChange returns reverting's change of tags.Env in the report.
func envChange(t *testing.T, d report.Drift) report.DriftChange {
	t.Helper()
	for _, c := range d.Changes {
		if c.Attribute == "tags.Env" {
			return c
		}
	}
	t.Fatalf("no change of tags.Env in %+v", d.Changes)
	return report.DriftChange{}
}

func TestDriftAppliesDecisions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		fingerprint string
		decision    string
		rules       []driftpolicy.Rule
		outcome     string
		source      string
		stale       bool
	}{
		{"accepted", envFingerprint, engine.DriftAccept, nil, report.DriftWritten, report.DriftDecidedByEntry, false},
		{"kept", envFingerprint, engine.DriftKeep, nil, report.DriftKept, report.DriftDecidedByEntry, false},
		{"changed again", decisionFingerprint("prod", "staging"), engine.DriftAccept, nil, report.DriftKept, report.DriftDecidedByEntry, true},
		{"changed again, accepted by --accept", decisionFingerprint("prod", "staging"), engine.DriftKeep, []driftpolicy.Rule{{Accept: true}}, report.DriftWritten, report.DriftDecidedByFlag, true},
		{"kept by --keep", envFingerprint, engine.DriftAccept, []driftpolicy.Rule{{Field: "attribute", Pattern: "tags.*"}}, report.DriftKept, report.DriftDecidedByFlag, false},
		{"accepted by --accept", envFingerprint, engine.DriftKeep, []driftpolicy.Rule{{Accept: true}}, report.DriftWritten, report.DriftDecidedByFlag, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := driftOutput(t, "aws/1/eu-west-2")
			f := &fakeDriftCheck{out: out, results: map[string]*engine.RootDrift{"aws/1/eu-west-2": reverting}, accepted: report.DriftWritten}
			options := driftOptions(out, f)
			options.Decisions = writeDecisions(t, t.TempDir(), tc.fingerprint, tc.decision)
			options.Rules = tc.rules
			options.WriteDecisions = true
			var stdout bytes.Buffer
			if err := Drift(context.Background(), options, &stdout); err != nil {
				t.Fatal(err)
			}
			d := readDrift(t, out)
			c := envChange(t, d)
			if c.Outcome != tc.outcome || c.DecisionSource != tc.source || c.DecisionStale != tc.stale {
				t.Errorf("tags.Env: %s from %s, stale %v, want %s from %s, stale %v", c.Outcome, c.DecisionSource, c.DecisionStale, tc.outcome, tc.source, tc.stale)
			}
			markdown, err := os.ReadFile(filepath.Join(out, report.Dir, report.DriftMarkdownFile))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(markdown), "changed again since its entry in the decisions file"); got != tc.stale {
				t.Errorf("drift.md says it changed again: %v, want %v", got, tc.stale)
			}

			// The file it applied is merged: a stale entry becomes
			// undecided, with the change's fingerprint now.
			g, err := driftpolicy.LoadDecisions(options.Decisions)
			if err != nil {
				t.Fatal(err)
			}
			want := driftpolicy.Entry{Root: "aws/1/eu-west-2", Address: "aws_s3_bucket.logs", Attribute: "tags.Env", Fingerprint: envFingerprint, Decision: tc.decision, Note: "mine"}
			if tc.stale {
				want.Decision = driftpolicy.Undecided
			}
			if len(g.Changes) != 1 || g.Changes[0] != want {
				t.Errorf("decisions file %+v, want %+v", g.Changes, want)
			}
			if !strings.Contains(stdout.String(), "decisions: "+options.Decisions) {
				t.Errorf("stdout doesn't say where the decisions went:\n%s", stdout.String())
			}
		})
	}
}

// --write-decisions without --decisions writes drift.yaml in the output
// directory, merging what is there without applying it.
func TestDriftWritesDecisions(t *testing.T) {
	t.Parallel()
	out := driftOutput(t, "aws/1/eu-west-2", "aws/1/global")
	f := &fakeDriftCheck{out: out, results: map[string]*engine.RootDrift{"aws/1/eu-west-2": reverting}, accepted: report.DriftWritten}
	options := driftOptions(out, f)
	options.WriteDecisions = true
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(out, driftpolicy.DefaultDecisionsFile)
	g, err := driftpolicy.LoadDecisions(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []driftpolicy.Entry{{Root: "aws/1/eu-west-2", Address: "aws_s3_bucket.logs", Attribute: "tags.Env", Fingerprint: envFingerprint, Decision: driftpolicy.Undecided}}
	if g.Defaults.Decision != engine.DriftKeep || !reflect.DeepEqual(g.Changes, want) {
		t.Errorf("wrote %+v, want the kept change undecided, and not the absorbed one", g)
	}

	// Decided there, it is still not applied without --decisions.
	writeDecisions(t, out, envFingerprint, engine.DriftAccept)
	if err := os.Rename(filepath.Join(out, "decisions.yaml"), path); err != nil {
		t.Fatal(err)
	}
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if c := envChange(t, readDrift(t, out)); c.Outcome != report.DriftKept || c.DecisionSource != report.DriftDecidedByDefault {
		t.Errorf("tags.Env: %s from %s, want kept by default", c.Outcome, c.DecisionSource)
	}
	if g, err := driftpolicy.LoadDecisions(path); err != nil || len(g.Changes) != 1 || g.Changes[0].Decision != engine.DriftAccept {
		t.Errorf("decisions file %+v (%v), want the decision kept", g, err)
	}

	// A file it can't merge into stops it before any root is checked.
	if err := os.WriteFile(path, []byte("version: 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.checked = nil
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--write-decisions") {
		t.Errorf("got %v, want the file refused", err)
	}
	if len(f.checked) > 0 {
		t.Error("checked roots anyway")
	}
}

// The decisions file holds no value of a change, nor a hash of one
// alone: only the fingerprint of the change as the report shows it.
func TestDriftDecisionsHoldNoSecrets(t *testing.T) {
	t.Parallel()
	const before, after = "hunter2-before-0123456789", "s3cr3t-after-9876543210"
	result := &engine.RootDrift{Backend: "s3", Resources: 1, Changes: []engine.DriftChange{
		{Address: "aws_db_instance.main", Type: "aws_db_instance", Attribute: "password", Before: before, After: after, Kind: engine.DriftChanged, Outcome: engine.DriftKept},
		{Address: "aws_db_instance.main", Type: "aws_db_instance", Attribute: "tags.Token", Before: map[string]any{"k": before}, After: []any{after}, Kind: engine.DriftChanged, Outcome: engine.DriftKept},
	}}
	out := driftOutput(t, "aws/1/eu-west-2")
	f := &fakeDriftCheck{out: out, results: map[string]*engine.RootDrift{"aws/1/eu-west-2": result}, accepted: report.DriftSecret}
	options := driftOptions(out, f)
	options.WriteDecisions = true
	options.Rules = []driftpolicy.Rule{{Accept: true}}
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(out, driftpolicy.DefaultDecisionsFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{before, after, "hunter2", "s3cr3t"} {
		sum := sha256.Sum256([]byte(v))
		if strings.Contains(string(content), v) || strings.Contains(string(content), hex.EncodeToString(sum[:])) {
			t.Errorf("the decisions file has %q, or its hash:\n%s", v, content)
		}
	}
	if strings.Count(string(content), "fingerprint: ") != 2 {
		t.Errorf("want an entry per change:\n%s", content)
	}
}

// --write-decisions=<file> writes that file, merging what it has without
// applying it, and leaves the output directory's drift.yaml alone. With
// --decisions, it has to name the same file.
func TestDriftWritesDecisionsToAFile(t *testing.T) {
	t.Parallel()
	out := driftOutput(t, "aws/1/eu-west-2")
	f := &fakeDriftCheck{out: out, results: map[string]*engine.RootDrift{"aws/1/eu-west-2": reverting}, accepted: report.DriftWritten}
	options := driftOptions(out, f)
	options.WriteDecisions = true
	options.WriteDecisionsTo = writeDecisions(t, t.TempDir(), envFingerprint, engine.DriftAccept)
	var stdout bytes.Buffer
	if err := Drift(context.Background(), options, &stdout); err != nil {
		t.Fatal(err)
	}
	if c := envChange(t, readDrift(t, out)); c.Outcome != report.DriftKept || c.DecisionSource != report.DriftDecidedByDefault {
		t.Errorf("tags.Env: %s from %s, want kept by default", c.Outcome, c.DecisionSource)
	}
	g, err := driftpolicy.LoadDecisions(options.WriteDecisionsTo)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Changes) != 1 || g.Changes[0].Decision != engine.DriftAccept || g.Changes[0].Note != "mine" {
		t.Errorf("decisions file %+v, want the entry's decision and note kept", g.Changes)
	}
	if !strings.Contains(stdout.String(), "decisions: "+options.WriteDecisionsTo) {
		t.Errorf("stdout doesn't say where the decisions went:\n%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(out, driftpolicy.DefaultDecisionsFile)); !os.IsNotExist(err) {
		t.Errorf("wrote %s in the output directory too (%v)", driftpolicy.DefaultDecisionsFile, err)
	}

	// Naming the file --decisions names, it applies it.
	options.Decisions = options.WriteDecisionsTo
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if c := envChange(t, readDrift(t, out)); c.Outcome != report.DriftWritten {
		t.Errorf("tags.Env with --decisions: %s, want written", c.Outcome)
	}

	f.checked = nil
	options.WriteDecisionsTo = filepath.Join(t.TempDir(), "other.yaml")
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--write-decisions") {
		t.Errorf("another file than --decisions: got %v, want it refused", err)
	}
	if len(f.checked) > 0 {
		t.Error("checked roots anyway")
	}
}

// A decisions file decides the changes of a root environments share by
// environment: an entry of one environment's change, at an address and
// attribute another environment's change has too, decides only its own,
// and --write-decisions keeps an entry per environment.
func TestDriftDecidesEachEnvironmentOfAMergedRoot(t *testing.T) {
	t.Parallel()
	out := driftOutput(t, "aws/us-east-1")
	for _, env := range []string{"dev", "prod"} {
		path := filepath.Join(out, "aws", "us-east-1", "vars", env, "terraform.tfvars")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("env = \""+env+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	decisions := filepath.Join(t.TempDir(), "decisions.yaml")
	content := "version: 1\ndefaults: { decision: accept }\nchanges:\n" +
		"  - { root: aws/us-east-1, environment: dev, address: aws_s3_bucket.logs, attribute: tags.Env, fingerprint: " + envFingerprint + ", decision: keep }\n"
	if err := os.WriteFile(decisions, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeDriftCheck{out: out, results: map[string]*engine.RootDrift{"aws/us-east-1": reverting}, accepted: report.DriftWritten}
	options := driftOptions(out, f)
	options.Decisions, options.WriteDecisions = decisions, true
	options.Environments = []string{config.AllEnvironments}
	var stdout bytes.Buffer
	if err := Drift(context.Background(), options, &stdout); err != nil {
		t.Fatal(err)
	}
	got := map[string]report.DriftChange{}
	for _, c := range readDrift(t, out).Changes {
		if c.Attribute == "tags.Env" {
			got[c.Environment] = c
		}
	}
	if c := got["dev"]; c.Outcome != report.DriftKept || c.DecisionSource != report.DriftDecidedByEntry {
		t.Errorf("dev: %s by %s, want %s by %s", c.Outcome, c.DecisionSource, report.DriftKept, report.DriftDecidedByEntry)
	}
	if c := got["prod"]; c.Outcome != report.DriftWritten || c.DecisionSource != report.DriftDecidedByDefaults {
		t.Errorf("prod: %s by %s, want %s by %s", c.Outcome, c.DecisionSource, report.DriftWritten, report.DriftDecidedByDefaults)
	}
	written, err := driftpolicy.LoadDecisions(decisions)
	if err != nil {
		t.Fatal(err)
	}
	want := []driftpolicy.Entry{
		{Root: "aws/us-east-1", Environment: "dev", Address: "aws_s3_bucket.logs", Attribute: "tags.Env", Fingerprint: envFingerprint, Decision: engine.DriftKeep},
		{Root: "aws/us-east-1", Environment: "prod", Address: "aws_s3_bucket.logs", Attribute: "tags.Env", Fingerprint: envFingerprint, Decision: driftpolicy.Undecided},
	}
	if !reflect.DeepEqual(written.Changes, want) {
		t.Errorf("decisions file\n%+v\nwant\n%+v", written.Changes, want)
	}
}
