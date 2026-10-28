// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package driftpolicy_test

import (
	"path/filepath"
	"testing"

	"github.com/IgnatG/infraharvest/internal/driftpolicy"
	"github.com/IgnatG/infraharvest/internal/engine/drift"
	"github.com/IgnatG/infraharvest/internal/report"
)

// saved returns f as drift reads it back, once Save has written it.
func saved(t *testing.T, f *driftpolicy.Decisions) *driftpolicy.Decisions {
	t.Helper()
	path := filepath.Join(t.TempDir(), driftpolicy.DefaultDecisionsFile)
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	g, err := driftpolicy.LoadDecisions(path)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// The decisions file decides first: its defaults, its rules, the last
// that matches, then a change's own entry; --accept and --keep decide
// after it.
func TestDecisionPrecedence(t *testing.T) {
	t.Parallel()
	const root = "aws/1/eu-west-2"
	env := report.DriftChange{Root: root, Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "tags.Env", Before: "prod", After: "dev", Kind: drift.Changed}
	owner := report.DriftChange{Root: root, Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Attribute: "tags.Owner", Before: "a", After: "b", Kind: drift.Changed}
	cidr := report.DriftChange{Root: root, Address: "aws_security_group.web", Type: "aws_security_group", Attribute: "ingress[0].cidr_blocks", Before: []any{"10.0.0.0/8"}, After: []any{"0.0.0.0/0"}, Kind: drift.Changed}
	file := func(defaults string, entries ...driftpolicy.Entry) *driftpolicy.Decisions {
		rules := []driftpolicy.DecisionRule{{Accept: &driftpolicy.Match{Attribute: []string{"tags.*"}}}, {Keep: &driftpolicy.Match{Attribute: []string{"tags.Owner"}, Type: []string{"aws_s3_*"}}}}
		return saved(t, &driftpolicy.Decisions{Version: 1, Defaults: driftpolicy.Defaults{Decision: defaults}, Rules: rules, Changes: entries})
	}
	entry := func(c report.DriftChange, decision string) driftpolicy.Entry {
		return driftpolicy.Entry{Root: root, Address: c.Address, Attribute: c.Attribute, Fingerprint: fingerprint(c), Decision: decision}
	}
	withAfter := func(c report.DriftChange, after any) report.DriftChange {
		c.After = after
		return c
	}
	stale := entry(cidr, report.DriftAccept)
	stale.Fingerprint = fingerprint(withAfter(cidr, []any{"10.1.0.0/16"}))
	staleEnv := entry(env, report.DriftAccept)
	staleEnv.Fingerprint = fingerprint(withAfter(env, "qa"))
	keepAll := []driftpolicy.Rule{{Accept: false}}
	acceptTags := []driftpolicy.Rule{{Accept: true, Field: "attribute", Pattern: "tags.Owner"}}
	for _, tc := range []struct {
		name   string
		d      driftpolicy.Decider
		c      report.DriftChange
		accept bool
		source string
		stale  bool
	}{
		{"nothing", driftpolicy.Decider{}, env, false, report.DriftDecidedByDefault, false},
		{"flags alone", driftpolicy.Decider{Rules: acceptTags}, owner, true, report.DriftDecidedByFlag, false},
		{"defaults", driftpolicy.Decider{Decisions: file(report.DriftKeep)}, cidr, false, report.DriftDecidedByDefaults, false},
		{"defaults accept", driftpolicy.Decider{Decisions: file(report.DriftAccept)}, cidr, true, report.DriftDecidedByDefaults, false},
		{"empty defaults keep", driftpolicy.Decider{Decisions: file("")}, cidr, false, report.DriftDecidedByDefaults, false},
		{"a rule", driftpolicy.Decider{Decisions: file(report.DriftKeep)}, env, true, report.DriftDecidedByRule, false},
		{"the last rule", driftpolicy.Decider{Decisions: file(report.DriftKeep)}, owner, false, report.DriftDecidedByRule, false},
		{"the entry over the rules", driftpolicy.Decider{Decisions: file(report.DriftKeep, entry(env, report.DriftKeep))}, env, false, report.DriftDecidedByEntry, false},
		{"the entry over the defaults", driftpolicy.Decider{Decisions: file(report.DriftKeep, entry(cidr, report.DriftAccept))}, cidr, true, report.DriftDecidedByEntry, false},
		{"an undecided entry", driftpolicy.Decider{Decisions: file(report.DriftKeep, entry(env, driftpolicy.Undecided))}, env, true, report.DriftDecidedByRule, false},
		{"a stale entry", driftpolicy.Decider{Decisions: file(report.DriftKeep, stale)}, cidr, false, report.DriftDecidedByEntry, true},
		{"a stale entry over accepting defaults", driftpolicy.Decider{Decisions: file(report.DriftAccept, stale)}, cidr, false, report.DriftDecidedByEntry, true},
		{"a stale entry over an accepting rule", driftpolicy.Decider{Decisions: file(report.DriftKeep, staleEnv)}, env, false, report.DriftDecidedByEntry, true},
		{"--accept over a stale entry", driftpolicy.Decider{Decisions: file(report.DriftKeep, stale), Rules: []driftpolicy.Rule{{Accept: true}}}, cidr, true, report.DriftDecidedByFlag, true},
		{"flags over the entry", driftpolicy.Decider{Decisions: file(report.DriftKeep, entry(cidr, report.DriftAccept)), Rules: keepAll}, cidr, false, report.DriftDecidedByFlag, false},
		{"flags over the rules", driftpolicy.Decider{Decisions: file(report.DriftKeep), Rules: acceptTags}, owner, true, report.DriftDecidedByFlag, false},
		{"flags that don't match", driftpolicy.Decider{Decisions: file(report.DriftKeep, entry(cidr, report.DriftAccept)), Rules: acceptTags}, cidr, true, report.DriftDecidedByEntry, false},
		{"another root's entry", driftpolicy.Decider{Decisions: file(report.DriftKeep, driftpolicy.Entry{Root: "aws/2/eu-west-2", Address: cidr.Address, Attribute: cidr.Attribute, Fingerprint: fingerprint(cidr), Decision: report.DriftAccept})}, cidr, false, report.DriftDecidedByDefaults, false},
		{"another environment's entry", driftpolicy.Decider{Decisions: file(report.DriftKeep, driftpolicy.Entry{Root: root, Environment: "prod", Address: cidr.Address, Attribute: cidr.Attribute, Fingerprint: fingerprint(cidr), Decision: report.DriftAccept})}, cidr, false, report.DriftDecidedByDefaults, false},
	} {
		got := tc.d.Decide(tc.c)
		if got.Accept != tc.accept || got.Source != tc.source || got.Stale != tc.stale {
			t.Errorf("%s: %+v, want accept %v from %s, stale %v", tc.name, got, tc.accept, tc.source, tc.stale)
		}
	}
}

// A file rule matches a change of shared tags as an --accept rule does.
func TestMatchSharedTags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		m    driftpolicy.Match
		want bool
	}{
		{driftpolicy.Match{}, true},
		{driftpolicy.Match{Type: []string{"aws_sqs_queue"}}, true},
		{driftpolicy.Match{Type: []string{"aws_instance", "aws_s3_bucket"}}, true},
		{driftpolicy.Match{Type: []string{"aws_instance"}}, false},
		{driftpolicy.Match{Address: []string{"local.tags"}, Attribute: []string{"tags.*"}}, true},
		{driftpolicy.Match{Address: []string{"local.tags"}, Attribute: []string{"labels.*"}}, false},
		{driftpolicy.Match{Root: []string{"google/*"}}, false},
	} {
		f := &driftpolicy.Decisions{Version: 1, Defaults: driftpolicy.Defaults{Decision: report.DriftKeep}, Rules: []driftpolicy.DecisionRule{{Accept: &tc.m}}}
		if got := (driftpolicy.Decider{Decisions: saved(t, f)}).Decide(sharedTags).Accept; got != tc.want {
			t.Errorf("%+v: %v, want %v", tc.m, got, tc.want)
		}
	}
}

func TestActive(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		d    driftpolicy.Decider
		want bool
	}{
		{driftpolicy.Decider{}, false},
		{driftpolicy.Decider{Rules: []driftpolicy.Rule{{}}}, true},
		{driftpolicy.Decider{Decisions: driftpolicy.NewDecisions()}, true},
	} {
		if got := tc.d.Active(); got != tc.want {
			t.Errorf("%+v: active %v, want %v", tc.d, got, tc.want)
		}
	}
}
