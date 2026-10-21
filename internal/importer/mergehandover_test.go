// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// mergedEarlier records, under out, the merged root fake/stack that an
// earlier import merged from the roots of dev and prod, which it set
// aside, with imports, the import IDs of each environment's resources in
// it by address. It returns the merged root's directory and dev's root.
func mergedEarlier(t *testing.T, out string, imports map[string]map[string]string) (stack, dev string) {
	t.Helper()
	stack = filepath.Join(out, "fake", "stack")
	dev = filepath.Join(out, "envs", "dev", "fake")
	writeRootFiles(t, stack, map[string]string{"main.tf": "# merged\n"})
	c := mergeCheckpoint{
		Members: []mergeCheckpointMember{{Env: "dev", Path: "envs/dev/fake"}, {Env: "prod", Path: "envs/prod/fake"}},
		Merge:   report.Merge{Path: "fake/stack", Merged: true},
		Imports: imports,
	}
	if err := writeMergeCheckpoint(out, stack, c); err != nil {
		t.Fatal(err)
	}
	return stack, dev
}

// What leaves a merged root for another root of the run is handed over
// by a removed block of the merged root: a whole resource only one
// environment has, and, of a for_each another environment keeps, the
// instance that goes alone, moved to a resource of the root alone first.
// The root it goes to imports it, knowing where from.
func TestPlanMergedHandovers(t *testing.T) {
	out := t.TempDir()
	stack, dev := mergedEarlier(t, out, map[string]map[string]string{
		"dev":  {"fake_thing.orders": "o1", "fake_thing.reports_dev[0]": "r1", `fake_thing.this["audit"]`: "a1"},
		"prod": {"fake_thing.orders": "o2", `fake_thing.this["alerts"]`: "a2"},
	})
	shared := filepath.Join(out, "envs", "shared", "fake")
	c := regenerating(out, map[string][]engine.Import{dev: {thingImport("o1")}, shared: {thingImport("r1"), thingImport("a1")}})

	if err := planHandovers(out, []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	h := c.handovers
	if err := h.refusal(dev); err != nil {
		t.Fatalf("dev refused: %v", err)
	}
	want := []engine.Removal{
		{From: "fake_thing.reports_dev", Resources: []engine.Departure{{Type: "fake_thing", ID: "r1", Address: "fake_thing.reports_dev[0]", Root: "envs/shared/fake"}}},
		{From: "fake_thing.departed_this_audit", Instance: `fake_thing.this["audit"]`, Resources: []engine.Departure{{Type: "fake_thing", ID: "a1", Address: `fake_thing.this["audit"]`, Root: "envs/shared/fake"}}},
	}
	if got := h.merged[stack]; !reflect.DeepEqual(got, want) {
		t.Errorf("the merged root's removals: got %+v, want %+v", got, want)
	}
	if got := h.arrivalsOf(shared); len(got) != 2 || got[0].Root != "fake/stack" || got[0].From != "fake_thing.reports_dev[0]" || got[1].From != `fake_thing.this["audit"]` {
		t.Errorf("shared's arrivals: %+v", got)
	}
	if len(c.byDir[shared]) != 2 || len(c.run.report.Excluded) != 0 {
		t.Errorf("shared imports %+v, excluded %+v", c.byDir[shared], c.run.report.Excluded)
	}
}

// A resource of a merged root another environment has at the same
// address can't leave in one environment: a removed block would make
// every environment's state forget it. The environment's root is left as
// it was, and what it would hand over stays in the merged root. So does
// what an environment whose root the run doesn't generate would hand
// over.
func TestPlanMergedHandoversKeepsWhatAnotherEnvironmentHas(t *testing.T) {
	out := t.TempDir()
	_, dev := mergedEarlier(t, out, map[string]map[string]string{
		"dev":  {"fake_thing.orders": "o1", "fake_thing.events": "e1"},
		"prod": {"fake_thing.orders": "o2"},
	})
	shared := filepath.Join(out, "envs", "shared", "fake")
	c := regenerating(out, map[string][]engine.Import{dev: {thingImport("e1")}, shared: {thingImport("o1"), thingImport("s1")}})
	if err := planHandovers(out, []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	if err := c.handovers.refusal(dev); err == nil || !strings.Contains(err.Error(), "another environment keeps") {
		t.Errorf("dev's root: %v, want it left as it was", err)
	}
	if got := excludedIDs(c.run); !slices.Equal(got, []string{"o1 fake/stack"}) {
		t.Errorf("excluded %v, want o1, held by fake/stack", got)
	}

	c = regenerating(out, map[string][]engine.Import{shared: {thingImport("e1"), thingImport("s1")}})
	if err := planHandovers(out, []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	if got := excludedIDs(c.run); !slices.Equal(got, []string{"e1 fake/stack"}) {
		t.Errorf("without dev's root: excluded %v, want e1, held by fake/stack", got)
	}
}

// Merged again, the merged root writes the removed blocks planned for it
// of what the roots they go to imported. One of a resource it still has,
// as its environment's root wasn't generated again, isn't written, and
// its handover is incomplete; one a root didn't import keeps the merged
// root from merging again.
func TestMergedHandovers(t *testing.T) {
	out := t.TempDir()
	stack := filepath.Join(out, "fake", "stack")
	shared := filepath.Join(out, "envs", "shared", "fake")
	removal := engine.Removal{From: "fake_thing.reports_dev", Resources: []engine.Departure{{Type: "fake_thing", ID: "r1", Address: "fake_thing.reports_dev[0]", Root: "envs/shared/fake"}}}
	h := &handovers{out: out, merged: map[string][]engine.Removal{stack: {removal}}, generated: map[string]*engine.Result{}}
	members := []mergeMember{{env: "dev", handovers: h}, {env: "prod", handovers: h}}
	now := map[string]map[string]string{"dev": {"fake_thing.orders": "o1"}}

	if _, _, why := mergedHandovers(out, stack, members, now); !strings.Contains(why, "didn't import") {
		t.Errorf("a root that didn't import it: %q", why)
	}
	h.record(shared, &engine.Result{Arrived: []engine.Arrival{{Type: "fake_thing", ID: "r1", Root: "fake/stack", From: "fake_thing.reports_dev[0]", To: "fake_thing.reports"}}}, nil)
	removals, incomplete, why := mergedHandovers(out, stack, members, now)
	if why != "" || !reflect.DeepEqual(removals, []engine.Removal{removal}) || len(incomplete) != 0 {
		t.Errorf("handed over: %+v %+v %q", removals, incomplete, why)
	}
	now["dev"]["fake_thing.reports_dev[0]"] = "r1"
	removals, incomplete, why = mergedHandovers(out, stack, members, now)
	if why != "" || len(removals) != 0 || len(incomplete) != 1 || incomplete[0].FailedRoot != "fake/stack" {
		t.Errorf("still in the merged root: %+v %+v %q", removals, incomplete, why)
	}
}

// An address that moves in one environment but stays in another can't
// move: a moved block moves it in every environment's state.
func TestMergedMovesInOneEnvironmentOnly(t *testing.T) {
	t.Parallel()
	before := map[string]map[string]string{
		"dev":  {"aws_sqs_queue.orders": "q1"},
		"prod": {"aws_sqs_queue.orders": "q2"},
	}
	now := map[string]map[string]string{
		"dev":  {`aws_sqs_queue.this["orders"]`: "q1"},
		"prod": {"aws_sqs_queue.orders": "q2"},
	}
	if _, why := mergedMoves(before, now, nil); !strings.Contains(why, "prod keeps it at aws_sqs_queue.orders") {
		t.Errorf("a move in dev alone: %q", why)
	}
}

// The removed blocks of a merged root's moves.tf from before stay, but
// for what it has again or a new removed block takes.
func TestStillRemoved(t *testing.T) {
	t.Parallel()
	kept := []engine.Removal{{From: "aws_sqs_queue.gone"}, {From: "aws_sqs_queue.back"}, {From: "aws_sqs_queue.again"}}
	now := map[string]map[string]string{"dev": {"aws_sqs_queue.back[0]": "q1"}}
	got := stillRemoved(kept, now, []engine.Removal{{From: "aws_sqs_queue.again"}})
	if !reflect.DeepEqual(got, []engine.Removal{{From: "aws_sqs_queue.gone"}}) {
		t.Errorf("still removed: %+v", got)
	}
}

// A merged root merged again with --regenerate writes the removed blocks
// of what it hands over into its moves.tf.
func TestMoveMergedHandsOver(t *testing.T) {
	stack := t.TempDir()
	writeRootFiles(t, stack, map[string]string{"main.tf": "resource \"aws_sqs_queue\" \"orders\" {\n  name = \"orders\"\n}\n", engine.ReadmeFileName: "# fake/stack\n"})
	r := NewRun()
	r.options.Regenerate = true
	before := map[string]map[string]string{"dev": {"aws_sqs_queue.orders": "q1", "aws_sqs_queue.reports": "q2"}}
	now := map[string]map[string]string{"dev": {"aws_sqs_queue.orders": "q1"}}
	result := &engine.MergeResult{Files: []string{"main.tf"}}
	removed := []engine.Removal{{From: "aws_sqs_queue.reports", Resources: []engine.Departure{{Type: "aws_sqs_queue", ID: "q2", Address: "aws_sqs_queue.reports", Root: "envs/shared/fake"}}}}
	if why, err := r.moveMerged(stack, result, before, now, nil, removed); why != "" || err != nil {
		t.Fatalf("moveMerged: %q, %v", why, err)
	}
	if got, err := engine.ReadRemoved(stack); err != nil || len(got) != 1 || got[0].From != "aws_sqs_queue.reports" {
		t.Errorf("moves.tf removes %+v, %v", got, err)
	}
	if !slices.Contains(result.Files, engine.MovesFileName) {
		t.Errorf("the merge's files: %v", result.Files)
	}
	if _, err := os.Stat(filepath.Join(stack, engine.MovesFileName)); err != nil {
		t.Error(err)
	}
}
