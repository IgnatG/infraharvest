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
	"github.com/IgnatG/infraharvest/internal/engine/envmerge"
	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/report"
)

// What leaves a merged root for another root of the run is handed over
// by a removed block of the merged root: a whole resource only one
// environment has, and, of a for_each another environment keeps, the
// instance that goes alone, moved to a resource of the root alone first.
// The root it goes to imports it, knowing where from.
func TestPlanMergedHandovers(t *testing.T) {
	out := t.TempDir()
	m := newMemoryOutput(out)
	stack, dev := m.mergedEarlier(map[string]map[string]string{
		"dev":  {"fake_thing.orders": "o1", "fake_thing.reports_dev[0]": "r1", `fake_thing.this["audit"]`: "a1"},
		"prod": {"fake_thing.orders": "o2", `fake_thing.this["alerts"]`: "a2"},
	})
	shared := filepath.Join(out, "envs", "shared", "fake")
	c := regenerating(out, map[string][]rootfile.Import{dev: {thingImport("o1")}, shared: {thingImport("r1"), thingImport("a1")}})

	if err := planHandovers(m.index(), []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	h := c.handovers
	if err := h.refusal(dev); err != nil {
		t.Fatalf("dev refused: %v", err)
	}
	want := []rootfile.Removal{
		{From: "fake_thing.reports_dev", Resources: []rootfile.Departure{{Type: "fake_thing", ID: "r1", Address: "fake_thing.reports_dev[0]", Root: "envs/shared/fake"}}},
		{From: "fake_thing.departed_this_audit", Instance: `fake_thing.this["audit"]`, Resources: []rootfile.Departure{{Type: "fake_thing", ID: "a1", Address: `fake_thing.this["audit"]`, Root: "envs/shared/fake"}}},
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
	m := newMemoryOutput(out)
	_, dev := m.mergedEarlier(map[string]map[string]string{
		"dev":  {"fake_thing.orders": "o1", "fake_thing.events": "e1"},
		"prod": {"fake_thing.orders": "o2"},
	})
	shared := filepath.Join(out, "envs", "shared", "fake")
	c := regenerating(out, map[string][]rootfile.Import{dev: {thingImport("e1")}, shared: {thingImport("o1"), thingImport("s1")}})
	if err := planHandovers(m.index(), []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	if err := c.handovers.refusal(dev); err == nil || !strings.Contains(err.Error(), "another environment keeps") {
		t.Errorf("dev's root: %v, want it left as it was", err)
	}
	if got := excludedIDs(c.run); !slices.Equal(got, []string{"o1 fake/stack"}) {
		t.Errorf("excluded %v, want o1, held by fake/stack", got)
	}

	c = regenerating(out, map[string][]rootfile.Import{shared: {thingImport("e1"), thingImport("s1")}})
	if err := planHandovers(m.index(), []*listedCall{c}); err != nil {
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
	removal := rootfile.Removal{From: "fake_thing.reports_dev", Resources: []rootfile.Departure{{Type: "fake_thing", ID: "r1", Address: "fake_thing.reports_dev[0]", Root: "envs/shared/fake"}}}
	h := &handovers{out: out, merged: map[string][]rootfile.Removal{stack: {removal}}, generated: map[string]*engine.Result{}}
	members := []mergeMember{{env: "dev", handovers: h}, {env: "prod", handovers: h}}
	now := map[string]map[string]string{"dev": {"fake_thing.orders": "o1"}}

	if _, _, why := mergedHandovers(out, stack, members, now); !strings.Contains(why, "didn't import") {
		t.Errorf("a root that didn't import it: %q", why)
	}
	h.record(shared, &engine.Result{Arrived: []rootfile.Arrival{{Type: "fake_thing", ID: "r1", Root: "fake/stack", From: "fake_thing.reports_dev[0]", To: "fake_thing.reports"}}}, nil)
	removals, incomplete, why := mergedHandovers(out, stack, members, now)
	if why != "" || !reflect.DeepEqual(removals, []rootfile.Removal{removal}) || len(incomplete) != 0 {
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
	kept := []rootfile.Removal{{From: "aws_sqs_queue.gone"}, {From: "aws_sqs_queue.back"}, {From: "aws_sqs_queue.again"}}
	now := map[string]map[string]string{"dev": {"aws_sqs_queue.back[0]": "q1"}}
	got := stillRemoved(kept, now, []rootfile.Removal{{From: "aws_sqs_queue.again"}})
	if !reflect.DeepEqual(got, []rootfile.Removal{{From: "aws_sqs_queue.gone"}}) {
		t.Errorf("still removed: %+v", got)
	}
}

// A merged root merged again with --regenerate writes the removed blocks
// of what it hands over into its moves.tf.
func TestMoveMergedHandsOver(t *testing.T) {
	stack := t.TempDir()
	writeRootFiles(t, stack, map[string]string{"main.tf": "resource \"aws_sqs_queue\" \"orders\" {\n  name = \"orders\"\n}\n", rootfile.ReadmeFileName: "# fake/stack\n"})
	p := newTestPhase("", nil)
	p.regenerate = true
	before := map[string]map[string]string{"dev": {"aws_sqs_queue.orders": "q1", "aws_sqs_queue.reports": "q2"}}
	now := map[string]map[string]string{"dev": {"aws_sqs_queue.orders": "q1"}}
	result := &envmerge.MergeResult{Files: []string{"main.tf"}}
	removed := []rootfile.Removal{{From: "aws_sqs_queue.reports", Resources: []rootfile.Departure{{Type: "aws_sqs_queue", ID: "q2", Address: "aws_sqs_queue.reports", Root: "envs/shared/fake"}}}}
	if why, err := p.moveMerged(stack, result, before, now, nil, removed); why != "" || err != nil {
		t.Fatalf("moveMerged: %q, %v", why, err)
	}
	if got, err := rootfile.ReadRemoved(stack); err != nil || len(got) != 1 || got[0].From != "aws_sqs_queue.reports" {
		t.Errorf("moves.tf removes %+v, %v", got, err)
	}
	if !slices.Contains(result.Files, rootfile.MovesFileName) {
		t.Errorf("the merge's files: %v", result.Files)
	}
	if _, err := os.Stat(filepath.Join(stack, rootfile.MovesFileName)); err != nil {
		t.Error(err)
	}
}

// vacatable records, under out, the merged root fake/stack that an
// earlier import merged from the roots of dev, in account 111, and prod,
// in account 222, each with an orders thing, o1 and o2. It returns the
// merged root's directory.
func vacatable(t *testing.T, out string) string {
	t.Helper()
	stack := filepath.Join(out, "fake", "stack")
	writeRootFiles(t, stack, map[string]string{
		"providers.tf":          "terraform {\n  required_version = \">= 1.5, < 2.0\"\n}\n",
		"main.tf":               "resource \"fake_thing\" \"orders\" {\n  name = \"orders\"\n}\n",
		rootfile.ReadmeFileName: "# fake/stack\n",
	})
	for _, env := range []string{"dev", "prod"} {
		writeRootFiles(t, filepath.Join(stack, "vars", env), map[string]string{"terraform.tfvars": "env = \"" + env + "\"\n", "imports.tfvars": "import_ids = {}\n"})
	}
	c := mergeCheckpoint{
		Members: []mergeCheckpointMember{{Env: "dev", Path: "envs/dev/fake", Account: "111"}, {Env: "prod", Path: "envs/prod/fake", Account: "222"}},
		Merge:   report.Merge{Path: "fake/stack", Merged: true},
		Files:   []string{rootfile.ReadmeFileName, "main.tf", "providers.tf", "vars/dev/imports.tfvars", "vars/dev/terraform.tfvars", "vars/prod/imports.tfvars", "vars/prod/terraform.tfvars"},
		Imports: map[string]map[string]string{"dev": {"fake_thing.orders": "o1"}, "prod": {"fake_thing.orders": "o2"}},
	}
	if err := mergeCheckpoints.write(out, stack, c); err != nil {
		t.Fatal(err)
	}
	return stack
}

// regeneratingIn is regenerating, as the call of the scope of account,
// of a command that imports scopes.
func regeneratingIn(out, account string, scopes []string, byDir map[string][]rootfile.Import) *listedCall {
	c := regenerating(out, byDir)
	c.scope = "fake/" + account + "/global"
	c.run.shared.scopes = scopes
	return c
}

// A merged root no root of the run merges into, such as after a changed
// --layout, all of whose resources go to roots of the run, is vacated:
// each scope hands over what its environment has there, the roots it
// goes to import it, and once every environment's have, the merged root
// keeps only the removed blocks that make each environment's state
// forget them.
func TestVacateMergedRoot(t *testing.T) {
	out := t.TempDir()
	stack := vacatable(t, out)
	dev, prod := filepath.Join(out, "envs", "dev", "new"), filepath.Join(out, "envs", "prod", "new")
	scopes := []string{"111", "222"}
	c1 := regeneratingIn(out, "111", scopes, map[string][]rootfile.Import{dev: {thingImport("o1")}})
	c2 := regeneratingIn(out, "222", scopes, map[string][]rootfile.Import{prod: {thingImport("o2")}})
	for _, c := range []*listedCall{c1, c2} {
		if err := planHandovers(newDiskIndex(out), []*listedCall{c}); err != nil {
			t.Fatal(err)
		}
		if len(c.run.report.Excluded) != 0 || len(c.run.vacating) != 1 {
			t.Errorf("%s: excluded %+v, vacating %d", c.scope, c.run.report.Excluded, len(c.run.vacating))
		}
	}
	if got := c1.handovers.arrivalsOf(dev); len(got) != 1 || got[0].Root != "fake/stack" || got[0].From != "fake_thing.orders" || got[0].ID != "o1" {
		t.Errorf("dev's arrivals: %+v", got)
	}
	c1.handovers.record(dev, &engine.Result{Arrived: []rootfile.Arrival{{Type: "fake_thing", ID: "o1", Root: "fake/stack", From: "fake_thing.orders", To: "fake_thing.orders"}}}, nil)
	c2.handovers.record(prod, &engine.Result{Arrived: []rootfile.Arrival{{Type: "fake_thing", ID: "o2", Root: "fake/stack", From: "fake_thing.orders", To: "fake_thing.orders"}}}, nil)

	p := newTestPhase(out, nil)
	p.vacating = slices.Concat(c1.run.vacating, c2.run.vacating)
	if err := p.vacateMergedRoots(nil); err != nil {
		t.Fatal(err)
	}
	if len(p.report.Failures) != 0 || len(p.report.IncompleteHandovers) != 0 {
		t.Fatalf("failures %v, incomplete %+v", p.report.Failures, p.report.IncompleteHandovers)
	}
	if _, err := os.Stat(filepath.Join(stack, "main.tf")); !os.IsNotExist(err) {
		t.Errorf("main.tf is still there: %v", err)
	}
	if got, err := rootfile.ReadRemoved(stack); err != nil || len(got) != 1 || got[0].From != "fake_thing.orders" {
		t.Errorf("moves.tf removes %+v, %v", got, err)
	}
	if len(p.report.Departed) != 2 || p.report.Departed[0].FromRoot != "fake/stack" {
		t.Errorf("departed %+v", p.report.Departed)
	}
	c, err := mergeCheckpoints.read(out, stack)
	if err != nil || c == nil || len(c.Imports) != 0 || !slices.Contains(c.Files, rootfile.MovesFileName) || slices.Contains(c.Files, "main.tf") {
		t.Errorf("the checkpoint: %+v, %v", c, err)
	}
}

// A merged root of which the run hands over only some environments'
// resources stays as it was, the run fails, and what the roots of the run
// imported is reported as an incomplete handover. Where the command
// doesn't import every environment's account, what an environment has
// stays in the merged root, left out of the roots it would go to.
func TestVacateMergedRootPartly(t *testing.T) {
	out := t.TempDir()
	stack := vacatable(t, out)
	dev := filepath.Join(out, "envs", "dev", "new")
	c := regeneratingIn(out, "111", []string{"111", "222"}, map[string][]rootfile.Import{dev: {thingImport("o1")}})
	if err := planHandovers(newDiskIndex(out), []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	c.handovers.record(dev, &engine.Result{Arrived: []rootfile.Arrival{{Type: "fake_thing", ID: "o1", Root: "fake/stack", From: "fake_thing.orders", To: "fake_thing.orders"}}}, nil)
	p := newTestPhase(out, nil)
	p.vacating = c.run.vacating
	if err := p.vacateMergedRoots(nil); err != nil {
		t.Fatal(err)
	}
	if len(p.report.Failures) != 1 || len(p.report.IncompleteHandovers) != 1 || p.report.IncompleteHandovers[0].FailedRoot != "fake/stack" {
		t.Errorf("failures %v, incomplete %+v", p.report.Failures, p.report.IncompleteHandovers)
	}
	if _, err := os.Stat(filepath.Join(stack, "main.tf")); err != nil {
		t.Errorf("main.tf: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stack, rootfile.MovesFileName)); !os.IsNotExist(err) {
		t.Errorf("moves.tf: %v", err)
	}

	c = regeneratingIn(out, "111", []string{"111"}, map[string][]rootfile.Import{dev: {thingImport("o1")}})
	if err := planHandovers(newDiskIndex(out), []*listedCall{c}); err != nil {
		t.Fatal(err)
	}
	if got := excludedIDs(c.run); !slices.Equal(got, []string{"o1 fake/stack"}) || len(c.run.vacating) != 0 {
		t.Errorf("without prod's account: excluded %v, vacating %d", got, len(c.run.vacating))
	}
}
