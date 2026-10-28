// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"slices"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// updating is a resource change that updates the attributes it names, from
// an empty value to a set one.
func updating(address, typ string, attributes ...string) *tfjson.ResourceChange {
	before, after := map[string]any{}, map[string]any{}
	for _, a := range attributes {
		before[a], after[a] = "old", "new"
	}
	rc := resourceChange(address, tfjson.Actions{tfjson.ActionUpdate}, before, after)
	rc.Type = typ
	return rc
}

// unchanged is a resource change that does nothing: an import as it was.
func unchanged(address, typ string) *tfjson.ResourceChange {
	rc := resourceChange(address, tfjson.Actions{tfjson.ActionNoop}, nil, nil)
	rc.Type = typ
	return rc
}

// baselineOf is the baseline of a plan of changes, with the totals summary.
func baselineOf(summary tfrun.ChangeSummary, stateOnly map[string][]string, changes ...*tfjson.ResourceChange) Baseline {
	b := newBaseline(shownPlan{&tfjson.Plan{ResourceChanges: changes}, &summary}, stateOnly)
	return b
}

func addresses(rcs []*tfjson.ResourceChange) []string {
	var got []string
	for _, rc := range rcs {
		got = append(got, rc.Address)
	}
	return got
}

// Each policy names how closely a plan must match the baseline: the same
// plan, one that only removes changes, or one that also updates arguments
// the provider keeps in state only.
func TestJudgePolicies(t *testing.T) {
	t.Parallel()
	stateOnly := map[string][]string{"aws_s3_bucket": {"force_destroy"}}
	// The baseline: a bucket that updates its tags, and a queue left as it is.
	baseline := baselineOf(tfrun.ChangeSummary{Change: 1, Import: 2}, stateOnly,
		updating("aws_s3_bucket.logs", "aws_s3_bucket", "tags"), unchanged("aws_sqs_queue.jobs", "aws_sqs_queue"))
	same := tfrun.ChangeSummary{Change: 1, Import: 2}

	for name, tt := range map[string]struct {
		policy  Policy
		changes []*tfjson.ResourceChange
		summary tfrun.ChangeSummary
		want    Verdict // without Changed, which is checked by address
		changed []string
	}{
		"exact, the same plan": {
			Exact, []*tfjson.ResourceChange{updating("aws_s3_bucket.logs", "aws_s3_bucket", "tags"), unchanged("aws_sqs_queue.jobs", "aws_sqs_queue")}, same,
			Verdict{Totals: true}, nil,
		},
		"exact, other totals": {
			Exact, []*tfjson.ResourceChange{updating("aws_s3_bucket.logs", "aws_s3_bucket", "tags"), unchanged("aws_sqs_queue.jobs", "aws_sqs_queue")}, tfrun.ChangeSummary{Import: 2},
			Verdict{Totals: false}, nil,
		},
		"exact, a resource that plans another change with the same totals": {
			Exact, []*tfjson.ResourceChange{updating("aws_s3_bucket.logs", "aws_s3_bucket", "versioning"), unchanged("aws_sqs_queue.jobs", "aws_sqs_queue")}, same,
			Verdict{Totals: true}, []string{"aws_s3_bucket.logs"},
		},
		"exact, a state-only update is a difference": {
			Exact, []*tfjson.ResourceChange{updating("aws_s3_bucket.logs", "aws_s3_bucket", "tags"), updating("aws_sqs_queue.jobs", "aws_sqs_queue", "force_destroy")}, tfrun.ChangeSummary{Change: 2, Import: 2},
			Verdict{Totals: false}, []string{"aws_sqs_queue.jobs"},
		},
		"exact, a resource the baseline lacks is left to the totals": {
			Exact, []*tfjson.ResourceChange{updating("aws_s3_bucket.logs", "aws_s3_bucket", "tags"), unchanged("aws_sqs_queue.jobs", "aws_sqs_queue"), updating("module.q.aws_sqs_queue.this", "aws_sqs_queue", "tags")}, same,
			Verdict{Totals: true}, nil,
		},
		"fewer changes, fewer updates": {
			FewerChanges, []*tfjson.ResourceChange{unchanged("aws_s3_bucket.logs", "aws_s3_bucket"), unchanged("aws_sqs_queue.jobs", "aws_sqs_queue")}, tfrun.ChangeSummary{Import: 2},
			Verdict{Totals: true}, nil,
		},
		"fewer changes, more updates": {
			FewerChanges, []*tfjson.ResourceChange{updating("aws_s3_bucket.logs", "aws_s3_bucket", "tags"), unchanged("aws_sqs_queue.jobs", "aws_sqs_queue")}, tfrun.ChangeSummary{Change: 2, Import: 2},
			Verdict{Totals: false}, nil,
		},
		"fewer changes, a resource that plans an attribute it didn't": {
			FewerChanges, []*tfjson.ResourceChange{updating("aws_s3_bucket.logs", "aws_s3_bucket", "tags", "versioning"), unchanged("aws_sqs_queue.jobs", "aws_sqs_queue")}, same,
			Verdict{Totals: true}, []string{"aws_s3_bucket.logs"},
		},
		"fewer changes, other adds or removes": {
			FewerChanges, []*tfjson.ResourceChange{unchanged("aws_s3_bucket.logs", "aws_s3_bucket"), unchanged("aws_sqs_queue.jobs", "aws_sqs_queue")}, tfrun.ChangeSummary{Add: 1, Import: 2},
			Verdict{Totals: false}, nil,
		},
		"fewer changes, other imports": {
			FewerChanges, []*tfjson.ResourceChange{unchanged("aws_s3_bucket.logs", "aws_s3_bucket"), unchanged("aws_sqs_queue.jobs", "aws_sqs_queue")}, tfrun.ChangeSummary{Import: 1},
			Verdict{Totals: true}, nil,
		},
		"fewer changes, a resource the baseline lacks that plans a change": {
			FewerChanges, []*tfjson.ResourceChange{unchanged("aws_s3_bucket.logs", "aws_s3_bucket"), unchanged("aws_sqs_queue.jobs", "aws_sqs_queue"), updating("aws_sqs_queue.other", "aws_sqs_queue", "tags")}, tfrun.ChangeSummary{Change: 1, Import: 2},
			Verdict{Totals: true}, []string{"aws_sqs_queue.other"},
		},
		"state-only, another type's argument is a difference": {
			StateOnly, []*tfjson.ResourceChange{updating("aws_s3_bucket.logs", "aws_s3_bucket", "tags"), updating("aws_sqs_queue.jobs", "aws_sqs_queue", "force_destroy")}, same,
			Verdict{Totals: true}, []string{"aws_sqs_queue.jobs"},
		},
		"state-only, the type's own argument": {
			StateOnly, []*tfjson.ResourceChange{updating("aws_s3_bucket.logs", "aws_s3_bucket", "tags", "force_destroy"), unchanged("aws_sqs_queue.jobs", "aws_sqs_queue")}, same,
			Verdict{Totals: true}, nil,
		},
		"state-only, an update of a resource that had none is counted": {
			StateOnly, []*tfjson.ResourceChange{updating("aws_s3_bucket.logs", "aws_s3_bucket", "tags"), updating("aws_sqs_queue.jobs", "aws_sqs_queue")}, same,
			Verdict{Totals: false, Updates: 1}, nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			summary := tt.summary
			got := baseline.Judge(shownPlan{&tfjson.Plan{ResourceChanges: tt.changes}, &summary}, nil, tt.policy)
			if got.Failed != tt.want.Failed || got.Totals != tt.want.Totals || got.Updates != tt.want.Updates {
				t.Errorf("got %+v, want failed=%v totals=%v updates=%d", got, tt.want.Failed, tt.want.Totals, tt.want.Updates)
			}
			if !slices.Equal(addresses(got.Changed), tt.changed) {
				t.Errorf("changed: got %v, want %v", addresses(got.Changed), tt.changed)
			}
			if want := tt.want.Totals && len(tt.changed) == 0; got.Accepted() != want {
				t.Errorf("accepted: got %v, want %v", got.Accepted(), want)
			}
		})
	}
}

// A state-only update of a resource that had none counts in the totals the
// plan is expected to have.
func TestJudgeStateOnlyCountsTheUpdatesItAllows(t *testing.T) {
	t.Parallel()
	baseline := baselineOf(tfrun.ChangeSummary{Import: 1}, map[string][]string{"aws_s3_bucket": {"force_destroy"}}, unchanged("aws_s3_bucket.logs", "aws_s3_bucket"))
	plan := &tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{updating("aws_s3_bucket.logs", "aws_s3_bucket", "force_destroy")}}

	for name, tt := range map[string]struct {
		summary  tfrun.ChangeSummary
		accepted bool
	}{
		"counted":   {tfrun.ChangeSummary{Change: 1, Import: 1}, true},
		"uncounted": {tfrun.ChangeSummary{Import: 1}, false},
	} {
		got := baseline.Judge(shownPlan{plan, &tt.summary}, nil, StateOnly)
		if got.Updates != 1 || got.Accepted() != tt.accepted {
			t.Errorf("%s: got %+v, want updates=1 accepted=%v", name, got, tt.accepted)
		}
	}
}

// A plan with errors, or none, has nothing to compare, whatever the policy.
func TestJudgeFailsWithoutAPlan(t *testing.T) {
	t.Parallel()
	baseline := baselineOf(tfrun.ChangeSummary{}, nil)
	summary := tfrun.ChangeSummary{}
	for name, p := range map[string]struct {
		plan  shownPlan
		diags []tfjson.Diagnostic
	}{
		"errors":     {shownPlan{&tfjson.Plan{}, &summary}, []tfjson.Diagnostic{{Summary: "boom"}}},
		"no plan":    {shownPlan{nil, &summary}, nil},
		"no summary": {shownPlan{&tfjson.Plan{}, nil}, nil},
	} {
		for _, policy := range []Policy{Exact, FewerChanges, StateOnly} {
			if got := baseline.Judge(p.plan, p.diags, policy); !got.Failed || got.Accepted() {
				t.Errorf("%s, policy %d: got %+v, want failed", name, policy, got)
			}
		}
	}
}

// Rebase takes the totals and every resource's change from the plan,
// wherever the resource is, and keeps the state-only rules.
func TestRebaseFollowsResourcesIntoModules(t *testing.T) {
	t.Parallel()
	stateOnly := map[string][]string{"aws_s3_bucket": {"force_destroy"}}
	before := baselineOf(tfrun.ChangeSummary{Import: 2}, stateOnly, unchanged("aws_s3_bucket.logs", "aws_s3_bucket"), unchanged("aws_sqs_queue.jobs", "aws_sqs_queue"))
	moved := updating("module.logs.aws_s3_bucket.this[0]", "aws_s3_bucket", "force_destroy")
	plan := shownPlan{&tfjson.Plan{ResourceChanges: []*tfjson.ResourceChange{moved, unchanged("aws_sqs_queue.jobs", "aws_sqs_queue")}}, &tfrun.ChangeSummary{Change: 1, Import: 2}}

	after := before.Rebase(plan)

	if after.summary != *plan.summary {
		t.Errorf("summary: got %+v, want %+v", after.summary, *plan.summary)
	}
	if got, want := after.Planned("module.logs.aws_s3_bucket.this[0]"), tfrun.ChangeSignature(moved); got != want {
		t.Errorf("the moved resource: got %q, want %q", got, want)
	}
	if got := after.Planned("aws_s3_bucket.logs"); got != "" {
		t.Errorf("the resource it moved from: got %q, want none", got)
	}
	if !slices.Equal(after.stateOnly["aws_s3_bucket"], stateOnly["aws_s3_bucket"]) {
		t.Errorf("state-only rules: got %v", after.stateOnly)
	}
	if before.Planned("aws_s3_bucket.logs") == "" {
		t.Error("Rebase changed the baseline it was called on")
	}
}

func TestExpectedAddsImportsAndUpdates(t *testing.T) {
	t.Parallel()
	b := baselineOf(tfrun.ChangeSummary{Add: 1, Change: 2, Remove: 3, Import: 4}, nil)
	if got, want := b.Expected(5, 6), (tfrun.ChangeSummary{Add: 1, Change: 8, Remove: 3, Import: 9}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestSameButStateOnly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in, root      string
		stateOnly     []string
		same, updates bool
	}{
		{"no-op:", "no-op:", nil, true, false},
		{"update:tags", "update:tags", nil, true, false},
		{"update:labels", "no-op:", []string{"labels"}, true, true},
		{"update:labels", "no-op:", nil, false, false},
		{"update:labels,tags", "update:tags", []string{"labels"}, true, false},
		{"update:labels,retries", "no-op:", []string{"labels"}, false, false},
		{"delete,create:labels", "no-op:", []string{"labels"}, false, false},
	} {
		same, updates := sameButStateOnly(tc.in, tc.root, tc.stateOnly)
		if same != tc.same || updates != tc.updates {
			t.Errorf("sameButStateOnly(%q, %q, %v) = %v, %v; want %v, %v", tc.in, tc.root, tc.stateOnly, same, updates, tc.same, tc.updates)
		}
	}
}

func TestNoWorse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		now, before string
		want        bool
	}{
		{"no-op:", "no-op:", true},
		{"no-op:", "update:force_destroy", true},
		{"update:force_destroy", "update:force_destroy,tags", true},
		{"update:retries", "no-op:", false},
		{"update:force_destroy,retries", "update:force_destroy", false},
		{"delete,create:", "no-op:", false},
	} {
		if got := noWorse(tc.now, tc.before); got != tc.want {
			t.Errorf("noWorse(%q, %q) = %v", tc.now, tc.before, got)
		}
	}
}
