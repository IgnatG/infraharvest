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

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// envFingerprint is the fingerprint of reverting's change of tags.Env.
var envFingerprint = driftFingerprint(engine.DriftChanged, "prod", "dev", nil)

const sampleDecisions = `version: 1
defaults:
  decision: keep
rules:
  - accept: { attribute: "tags.*", type: [aws_s3_bucket, "aws_sqs_*"] }
  - keep: { root: "aws/*/global" }
  - accept: all
changes:
  - root: aws/1/eu-west-2
    address: aws_s3_bucket.logs
    attribute: tags.Env
    fingerprint: 3f2a
    decision: accept
    note: renamed in the console
  - root: aws/1/eu-west-2
    address: aws_s3_bucket.old
    fingerprint: 9b1c
    decision: undecided
`

func TestParseDriftDecisions(t *testing.T) {
	t.Parallel()
	f, err := parseDriftDecisions("drift.yaml", []byte(sampleDecisions))
	if err != nil {
		t.Fatal(err)
	}
	if f.Defaults.Decision != engine.DriftKeep || len(f.Rules) != 3 || len(f.Changes) != 2 {
		t.Fatalf("read %+v", f)
	}
	if got, want := *f.Rules[0].Accept, (DriftMatch{Attribute: selection.Patterns{"tags.*"}, Type: selection.Patterns{"aws_s3_bucket", "aws_sqs_*"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("rule 1 %+v, want %+v", got, want)
	}
	if f.Rules[1].Keep == nil || !reflect.DeepEqual(f.Rules[1].Keep.Root, selection.Patterns{"aws/*/global"}) {
		t.Errorf("rule 2 %+v", f.Rules[1])
	}
	if f.Rules[2].Accept == nil || !reflect.DeepEqual(*f.Rules[2].Accept, DriftMatch{}) {
		t.Errorf("rule 3 %+v, want all", f.Rules[2])
	}
	if want := (DriftDecisionEntry{Root: "aws/1/eu-west-2", Address: "aws_s3_bucket.logs", Attribute: "tags.Env", Fingerprint: "3f2a", Decision: engine.DriftAccept, Note: "renamed in the console"}); f.Changes[0] != want {
		t.Errorf("change 1 %+v, want %+v", f.Changes[0], want)
	}
	if f.byKey[driftDecisionKey("aws/1/eu-west-2", "aws_s3_bucket.old", "")] != &f.Changes[1] {
		t.Error("the deleted resource's change isn't indexed")
	}
}

func TestParseDriftDecisionsRefuses(t *testing.T) {
	t.Parallel()
	change := "  - { root: r, address: a, attribute: x, fingerprint: f, decision: keep }\n"
	for name, tc := range map[string]struct{ content, want string }{
		"empty":            {"", "empty"},
		"version":          {"version: 2\n", "version 2 isn't supported"},
		"unknown field":    {"version: 1\nchange: []\n", "field change not found"},
		"defaults":         {"version: 1\ndefaults: { decision: maybe }\n", "defaults: decision must be accept or keep"},
		"both in a rule":   {"version: 1\nrules:\n  - { accept: all, keep: all }\n", "rule 1 must have one of accept and keep"},
		"none in a rule":   {"version: 1\nrules:\n  - {}\n", "rule 1 must have one of accept and keep"},
		"null rule":        {"version: 1\nrules:\n  - accept:\n", "rule 1 must have one of accept and keep"},
		"rule field":       {"version: 1\nrules:\n  - accept: { name: x }\n", "has no field name"},
		"rule scalar":      {"version: 1\nrules:\n  - accept: tags.*\n", "a rule matches all, or a map"},
		"decision":         {"version: 1\nchanges:\n  - { root: r, address: a, fingerprint: f, decision: yes }\n", "decision must be accept, keep or undecided"},
		"no root":          {"version: 1\nchanges:\n  - { address: a, fingerprint: f, decision: keep }\n", "change 1 needs a root and an address"},
		"no fingerprint":   {"version: 1\nchanges:\n  - { root: r, address: a, decision: keep }\n", "needs the fingerprint"},
		"listed twice":     {"version: 1\nchanges:\n" + change + change, "change 2 (a x in r) is listed twice"},
		"change field":     {"version: 1\nchanges:\n  - { root: r, address: a, fingerprint: f, decision: keep, before: x }\n", "field before not found"},
		"not yaml":         {"version: [1\n", "drift.yaml"},
		"version missing":  {"defaults: { decision: keep }\n", "version 0 isn't supported"},
		"patterns not map": {"version: 1\nrules:\n  - accept: { type: { a: b } }\n", "drift.yaml"},
	} {
		_, err := parseDriftDecisions("drift.yaml", []byte(tc.content))
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "drift.yaml") {
			t.Errorf("%s: got %v, want an error naming the file with %q", name, err, tc.want)
		}
	}
}

// What Save writes, Load reads back the same.
func TestDriftDecisionsRoundTrip(t *testing.T) {
	t.Parallel()
	f, err := parseDriftDecisions("drift.yaml", []byte(sampleDecisions))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "drift.yaml")
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), "# infraharvest drift decisions") || !strings.Contains(string(content), "- accept: all\n") {
		t.Errorf("wrote:\n%s", content)
	}
	g, err := LoadDriftDecisions(path)
	if err != nil {
		t.Fatalf("%v in:\n%s", err, content)
	}
	if g.Version != f.Version || g.Defaults != f.Defaults || !reflect.DeepEqual(g.Rules, f.Rules) || !reflect.DeepEqual(g.Changes, f.Changes) {
		t.Errorf("read back %+v, want %+v", g, f)
	}
}

// The decisions file decides first: its defaults, its rules, the last
// that matches, then a change's own entry; --accept and --keep decide
// after it.
func TestDriftDecisionPrecedence(t *testing.T) {
	t.Parallel()
	const root = "aws/1/eu-west-2"
	env := engine.DriftChange{Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "tags.Env", Before: "prod", After: "dev", Kind: engine.DriftChanged}
	owner := engine.DriftChange{Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "tags.Owner", Before: "a", After: "b", Kind: engine.DriftChanged}
	cidr := engine.DriftChange{Address: "aws_security_group.web", Type: "aws_security_group", Attribute: "ingress[0].cidr_blocks", Before: []any{"10.0.0.0/8"}, After: []any{"0.0.0.0/0"}, Kind: engine.DriftChanged}
	file := func(defaults string, entries ...DriftDecisionEntry) *DriftDecisions {
		rules := []DriftDecisionRule{{Accept: &DriftMatch{Attribute: selection.Patterns{"tags.*"}}}, {Keep: &DriftMatch{Attribute: selection.Patterns{"tags.Owner"}, Type: selection.Patterns{"aws_s3_*"}}}}
		f := &DriftDecisions{Version: 1, Defaults: DriftDecisionDefaults{Decision: defaults}, Rules: rules, Changes: entries}
		f.index()
		return f
	}
	entry := func(c engine.DriftChange, decision string) DriftDecisionEntry {
		return DriftDecisionEntry{Root: root, Address: c.Address, Attribute: c.Attribute, Fingerprint: driftFingerprint(c.Kind, c.Before, c.After, c.Covers), Decision: decision}
	}
	stale := entry(cidr, engine.DriftAccept)
	stale.Fingerprint = driftFingerprint(cidr.Kind, cidr.Before, []any{"10.1.0.0/16"}, nil)
	keepAll := []DriftRule{{Accept: false}}
	acceptTags := []DriftRule{{Accept: true, Field: "attribute", Pattern: "tags.Owner"}}
	for _, tc := range []struct {
		name   string
		d      driftDecider
		c      engine.DriftChange
		accept bool
		source string
		stale  bool
	}{
		{"nothing", driftDecider{}, env, false, report.DriftDecidedByDefault, false},
		{"flags alone", driftDecider{rules: acceptTags}, owner, true, report.DriftDecidedByFlag, false},
		{"defaults", driftDecider{file: file(engine.DriftKeep)}, cidr, false, report.DriftDecidedByDefaults, false},
		{"defaults accept", driftDecider{file: file(engine.DriftAccept)}, cidr, true, report.DriftDecidedByDefaults, false},
		{"empty defaults keep", driftDecider{file: file("")}, cidr, false, report.DriftDecidedByDefaults, false},
		{"a rule", driftDecider{file: file(engine.DriftKeep)}, env, true, report.DriftDecidedByRule, false},
		{"the last rule", driftDecider{file: file(engine.DriftKeep)}, owner, false, report.DriftDecidedByRule, false},
		{"the entry over the rules", driftDecider{file: file(engine.DriftKeep, entry(env, engine.DriftKeep))}, env, false, report.DriftDecidedByEntry, false},
		{"the entry over the defaults", driftDecider{file: file(engine.DriftKeep, entry(cidr, engine.DriftAccept))}, cidr, true, report.DriftDecidedByEntry, false},
		{"an undecided entry", driftDecider{file: file(engine.DriftKeep, entry(env, DriftUndecided))}, env, true, report.DriftDecidedByRule, false},
		{"a stale entry", driftDecider{file: file(engine.DriftKeep, stale)}, cidr, false, report.DriftDecidedByDefaults, true},
		{"flags over the entry", driftDecider{file: file(engine.DriftKeep, entry(cidr, engine.DriftAccept)), rules: keepAll}, cidr, false, report.DriftDecidedByFlag, false},
		{"flags over the rules", driftDecider{file: file(engine.DriftKeep), rules: acceptTags}, owner, true, report.DriftDecidedByFlag, false},
		{"flags that don't match", driftDecider{file: file(engine.DriftKeep, entry(cidr, engine.DriftAccept)), rules: acceptTags}, cidr, true, report.DriftDecidedByEntry, false},
		{"another root's entry", driftDecider{file: file(engine.DriftKeep, DriftDecisionEntry{Root: "aws/2/eu-west-2", Address: cidr.Address, Attribute: cidr.Attribute, Fingerprint: entry(cidr, "").Fingerprint, Decision: engine.DriftAccept})}, cidr, false, report.DriftDecidedByDefaults, false},
	} {
		got := tc.d.decide(root, tc.c)
		if got.accept != tc.accept || got.source != tc.source || got.stale != tc.stale {
			t.Errorf("%s: %+v, want accept %v from %s, stale %v", tc.name, got, tc.accept, tc.source, tc.stale)
		}
	}
}

// A file rule matches a change of shared tags as an --accept rule does.
func TestDriftMatchSharedTags(t *testing.T) {
	t.Parallel()
	shared := engine.DriftChange{Address: "local.tags", Attribute: "tags.Env", Covers: []string{"aws_s3_bucket.logs", "module.queues.aws_sqs_queue.this[0]"}}
	for _, tc := range []struct {
		m    DriftMatch
		want bool
	}{
		{DriftMatch{}, true},
		{DriftMatch{Type: selection.Patterns{"aws_sqs_queue"}}, true},
		{DriftMatch{Type: selection.Patterns{"aws_instance", "aws_s3_bucket"}}, true},
		{DriftMatch{Type: selection.Patterns{"aws_instance"}}, false},
		{DriftMatch{Address: selection.Patterns{"local.tags"}, Attribute: selection.Patterns{"tags.*"}}, true},
		{DriftMatch{Address: selection.Patterns{"local.tags"}, Attribute: selection.Patterns{"labels.*"}}, false},
		{DriftMatch{Root: selection.Patterns{"google/*"}}, false},
	} {
		if got := tc.m.matches("aws/1/eu-west-2", shared); got != tc.want {
			t.Errorf("%+v: %v, want %v", tc.m, got, tc.want)
		}
	}
}

func TestDriftDecisionsMerge(t *testing.T) {
	t.Parallel()
	change := func(root, address, attribute, outcome string, after any) report.DriftChange {
		return report.DriftChange{Root: root, Address: address, Attribute: attribute, Kind: engine.DriftChanged, Before: "a", After: after, Outcome: outcome}
	}
	fp := func(after any) string { return driftFingerprint(engine.DriftChanged, "a", after, nil) }
	f := &DriftDecisions{Version: 1, Changes: []DriftDecisionEntry{
		{Root: "r1", Address: "x.same", Attribute: "tags.A", Fingerprint: fp("b"), Decision: engine.DriftAccept, Note: "keep me"},
		{Root: "r1", Address: "x.again", Attribute: "tags.A", Fingerprint: fp("b"), Decision: engine.DriftKeep, Note: "still mine"},
		{Root: "r1", Address: "x.gone", Attribute: "tags.A", Fingerprint: fp("b"), Decision: engine.DriftAccept},
		{Root: "r2", Address: "x.unchecked", Attribute: "tags.A", Fingerprint: fp("b"), Decision: engine.DriftAccept},
	}}
	f.index()
	added, undecided, dropped := f.Merge([]report.DriftChange{
		change("r1", "x.same", "tags.A", report.DriftWritten, "b"),
		change("r1", "x.again", "tags.A", report.DriftKept, "c"),
		change("r1", "x.new", "tags.A", report.DriftSecret, "b"),
		change("r1", "x.absorbed", "arn", report.DriftAbsorbed, "b"),
		change("r1", "x.unknown", "password", report.DriftNotAssessable, "b"),
	}, func(root string) bool { return root == "r1" })
	if added != 1 || undecided != 1 || dropped != 1 {
		t.Errorf("added %d, undecided %d, dropped %d, want 1, 1, 1", added, undecided, dropped)
	}
	want := []DriftDecisionEntry{
		{Root: "r1", Address: "x.same", Attribute: "tags.A", Fingerprint: fp("b"), Decision: engine.DriftAccept, Note: "keep me"},
		{Root: "r1", Address: "x.again", Attribute: "tags.A", Fingerprint: fp("c"), Decision: DriftUndecided, Note: "still mine"},
		{Root: "r1", Address: "x.new", Attribute: "tags.A", Fingerprint: fp("b"), Decision: DriftUndecided},
		{Root: "r2", Address: "x.unchecked", Attribute: "tags.A", Fingerprint: fp("b"), Decision: engine.DriftAccept},
	}
	if !reflect.DeepEqual(f.Changes, want) {
		t.Errorf("merged\n%+v\nwant\n%+v", f.Changes, want)
	}
	if f.byKey[driftDecisionKey("r1", "x.new", "tags.A")] == nil || f.byKey[driftDecisionKey("r1", "x.gone", "tags.A")] != nil {
		t.Error("the index isn't rebuilt")
	}
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
		rules       []DriftRule
		outcome     string
		source      string
		stale       bool
	}{
		{"accepted", envFingerprint, engine.DriftAccept, nil, report.DriftWritten, report.DriftDecidedByEntry, false},
		{"kept", envFingerprint, engine.DriftKeep, nil, report.DriftKept, report.DriftDecidedByEntry, false},
		{"changed again", driftFingerprint(engine.DriftChanged, "prod", "staging", nil), engine.DriftAccept, nil, report.DriftKept, report.DriftDecidedByDefaults, true},
		{"kept by --keep", envFingerprint, engine.DriftAccept, []DriftRule{{Field: "attribute", Pattern: "tags.*"}}, report.DriftKept, report.DriftDecidedByFlag, false},
		{"accepted by --accept", envFingerprint, engine.DriftKeep, []DriftRule{{Accept: true}}, report.DriftWritten, report.DriftDecidedByFlag, false},
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
			g, err := LoadDriftDecisions(options.Decisions)
			if err != nil {
				t.Fatal(err)
			}
			want := DriftDecisionEntry{Root: "aws/1/eu-west-2", Address: "aws_s3_bucket.logs", Attribute: "tags.Env", Fingerprint: envFingerprint, Decision: tc.decision, Note: "mine"}
			if tc.stale {
				want.Decision = DriftUndecided
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
	path := filepath.Join(out, DefaultDriftDecisionsFile)
	g, err := LoadDriftDecisions(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []DriftDecisionEntry{{Root: "aws/1/eu-west-2", Address: "aws_s3_bucket.logs", Attribute: "tags.Env", Fingerprint: envFingerprint, Decision: DriftUndecided}}
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
	if g, err := LoadDriftDecisions(path); err != nil || len(g.Changes) != 1 || g.Changes[0].Decision != engine.DriftAccept {
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
	options.Rules = []DriftRule{{Accept: true}}
	if err := Drift(context.Background(), options, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(out, DefaultDriftDecisionsFile))
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

// The fingerprint changes with the change's values, and with what a
// change of shared tags covers, and with nothing else.
func TestDriftFingerprint(t *testing.T) {
	t.Parallel()
	base := driftFingerprint(engine.DriftChanged, "prod", "dev", nil)
	if len(base) != 64 || base != driftFingerprint(engine.DriftChanged, "prod", "dev", nil) {
		t.Fatalf("fingerprint %q isn't a stable SHA-256", base)
	}
	if base != driftFingerprint(engine.DriftChanged, "prod", "dev", []string{}) {
		t.Error("no resources covered and none differ")
	}
	for name, other := range map[string]string{
		"before": driftFingerprint(engine.DriftChanged, "test", "dev", nil),
		"after":  driftFingerprint(engine.DriftChanged, "prod", "qa", nil),
		"kind":   driftFingerprint(engine.DriftDeleted, "prod", "dev", nil),
		"covers": driftFingerprint(engine.DriftChanged, "prod", "dev", []string{"aws_s3_bucket.logs"}),
		"type":   driftFingerprint(engine.DriftChanged, "prod", []any{"dev"}, nil),
	} {
		if other == base {
			t.Errorf("another %s, the same fingerprint", name)
		}
	}
	// Map keys are marshalled in order.
	a := driftFingerprint(engine.DriftChanged, nil, map[string]any{"a": 1.0, "b": 2.0}, nil)
	b := driftFingerprint(engine.DriftChanged, nil, map[string]any{"b": 2.0, "a": 1.0}, nil)
	if a != b {
		t.Error("the same map, another fingerprint")
	}
}
