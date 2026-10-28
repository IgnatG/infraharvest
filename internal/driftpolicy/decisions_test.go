// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package driftpolicy_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/driftpolicy"
	"github.com/IgnatG/infraharvest/internal/engine/drift"
	"github.com/IgnatG/infraharvest/internal/report"
)

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

func TestParseDecisions(t *testing.T) {
	t.Parallel()
	f, err := driftpolicy.ParseDecisions("drift.yaml", []byte(sampleDecisions))
	if err != nil {
		t.Fatal(err)
	}
	if f.Defaults.Decision != report.DriftKeep || len(f.Rules) != 3 || len(f.Changes) != 2 {
		t.Fatalf("read %+v", f)
	}
	if got, want := *f.Rules[0].Accept, (driftpolicy.Match{Attribute: []string{"tags.*"}, Type: []string{"aws_s3_bucket", "aws_sqs_*"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("rule 1 %+v, want %+v", got, want)
	}
	if f.Rules[1].Keep == nil || !reflect.DeepEqual(f.Rules[1].Keep.Root, []string{"aws/*/global"}) {
		t.Errorf("rule 2 %+v", f.Rules[1])
	}
	if f.Rules[2].Accept == nil || !reflect.DeepEqual(*f.Rules[2].Accept, driftpolicy.Match{}) {
		t.Errorf("rule 3 %+v, want all", f.Rules[2])
	}
	if want := (driftpolicy.Entry{Root: "aws/1/eu-west-2", Address: "aws_s3_bucket.logs", Attribute: "tags.Env", Fingerprint: "3f2a", Decision: report.DriftAccept, Note: "renamed in the console"}); f.Changes[0] != want {
		t.Errorf("change 1 %+v, want %+v", f.Changes[0], want)
	}
	// The deleted resource's entry, for other values than these, decides
	// it: it is found.
	old := report.DriftChange{Root: "aws/1/eu-west-2", Address: "aws_s3_bucket.old", Kind: report.DriftDeleted}
	if d := (driftpolicy.Decider{Decisions: f}).Decide(old); d.Source != report.DriftDecidedByEntry || !d.Stale {
		t.Errorf("the deleted resource's change: %+v, want its stale entry", d)
	}
}

func TestParseDecisionsRefuses(t *testing.T) {
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
		"patterns not map": {"version: 1\nrules:\n  - accept: { type: { a: b } }\n", "cannot unmarshal !!map into []string"},
	} {
		_, err := driftpolicy.ParseDecisions("drift.yaml", []byte(tc.content))
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "drift.yaml") {
			t.Errorf("%s: got %v, want an error naming the file with %q", name, err, tc.want)
		}
	}
}

// What Save writes, Load reads back the same.
func TestDecisionsRoundTrip(t *testing.T) {
	t.Parallel()
	f, err := driftpolicy.ParseDecisions("drift.yaml", []byte(sampleDecisions))
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
	g, err := driftpolicy.LoadDecisions(path)
	if err != nil {
		t.Fatalf("%v in:\n%s", err, content)
	}
	if g.Version != f.Version || g.Defaults != f.Defaults || !reflect.DeepEqual(g.Rules, f.Rules) || !reflect.DeepEqual(g.Changes, f.Changes) {
		t.Errorf("read back %+v, want %+v", g, f)
	}
}

// A new decisions file keeps every change, and writes the version, the
// defaults and no changes.
func TestNewDecisions(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), driftpolicy.DefaultDecisionsFile)
	if err := driftpolicy.NewDecisions().Save(path); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(content), "#\nversion: 1\ndefaults:\n  decision: keep\nchanges: []\n") {
		t.Errorf("wrote:\n%s", content)
	}
}

func TestDecisionsMerge(t *testing.T) {
	t.Parallel()
	change := func(root, address, attribute, outcome string, after any) report.DriftChange {
		return report.DriftChange{Root: root, Address: address, Attribute: attribute, Kind: drift.Changed, Before: "a", After: after, Outcome: outcome}
	}
	fp := func(after any) string { return fingerprint(change("r1", "x", "tags.A", report.DriftKept, after)) }
	f := &driftpolicy.Decisions{Version: 1, Changes: []driftpolicy.Entry{
		{Root: "r1", Address: "x.same", Attribute: "tags.A", Fingerprint: fp("b"), Decision: report.DriftAccept, Note: "keep me"},
		{Root: "r1", Address: "x.again", Attribute: "tags.A", Fingerprint: fp("b"), Decision: report.DriftKeep, Note: "still mine"},
		{Root: "r1", Address: "x.gone", Attribute: "tags.A", Fingerprint: fp("b"), Decision: report.DriftAccept},
		{Root: "r2", Address: "x.unchecked", Attribute: "tags.A", Fingerprint: fp("b"), Decision: report.DriftAccept},
	}}
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
	want := []driftpolicy.Entry{
		{Root: "r1", Address: "x.same", Attribute: "tags.A", Fingerprint: fp("b"), Decision: report.DriftAccept, Note: "keep me"},
		{Root: "r1", Address: "x.again", Attribute: "tags.A", Fingerprint: fp("c"), Decision: driftpolicy.Undecided, Note: "still mine"},
		{Root: "r1", Address: "x.new", Attribute: "tags.A", Fingerprint: fp("b"), Decision: driftpolicy.Undecided},
		{Root: "r2", Address: "x.unchecked", Attribute: "tags.A", Fingerprint: fp("b"), Decision: report.DriftAccept},
	}
	if !reflect.DeepEqual(f.Changes, want) {
		t.Errorf("merged\n%+v\nwant\n%+v", f.Changes, want)
	}
	// The merged file decides by its entries now: the new one's, for
	// other values, is stale, and the dropped one's decides nothing.
	d := driftpolicy.Decider{Decisions: f}
	if got := d.Decide(change("r1", "x.new", "tags.A", report.DriftKept, "z")); !got.Stale {
		t.Errorf("the added entry doesn't decide: %+v", got)
	}
	if got := d.Decide(change("r1", "x.gone", "tags.A", report.DriftKept, "b")); got.Source != report.DriftDecidedByDefaults {
		t.Errorf("the dropped entry still decides: %+v", got)
	}
}

// The fingerprint changes with the change's values, and with what a
// change of shared tags covers, and with nothing else.
func TestFingerprint(t *testing.T) {
	t.Parallel()
	of := func(kind string, before, after any, covers []string) string {
		return fingerprint(report.DriftChange{Root: "r", Address: "a", Attribute: "tags.Env", Kind: kind, Before: before, After: after, Covers: covers})
	}
	base := of(drift.Changed, "prod", "dev", nil)
	if len(base) != 64 || base != of(drift.Changed, "prod", "dev", nil) {
		t.Fatalf("fingerprint %q isn't a stable SHA-256", base)
	}
	if base != of(drift.Changed, "prod", "dev", []string{}) {
		t.Error("no resources covered and none differ")
	}
	for name, other := range map[string]string{
		"before": of(drift.Changed, "test", "dev", nil),
		"after":  of(drift.Changed, "prod", "qa", nil),
		"kind":   of(drift.Deleted, "prod", "dev", nil),
		"covers": of(drift.Changed, "prod", "dev", []string{"aws_s3_bucket.logs"}),
		"type":   of(drift.Changed, "prod", []any{"dev"}, nil),
	} {
		if other == base {
			t.Errorf("another %s, the same fingerprint", name)
		}
	}
	// Map keys are marshalled in order.
	a := of(drift.Changed, nil, map[string]any{"a": 1.0, "b": 2.0}, nil)
	b := of(drift.Changed, nil, map[string]any{"b": 2.0, "a": 1.0}, nil)
	if a != b {
		t.Error("the same map, another fingerprint")
	}
}

// fingerprint is the fingerprint Merge writes for c's entry.
func fingerprint(c report.DriftChange) string {
	f := driftpolicy.NewDecisions()
	c.Outcome = report.DriftKept
	f.Merge([]report.DriftChange{c}, nil)
	return f.Changes[0].Fingerprint
}
