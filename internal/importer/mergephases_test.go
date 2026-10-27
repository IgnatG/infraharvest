// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// remergeStack is the stack app of dev and prod under out, merged before:
// its merged root holds main.tf, and dev's resources there are queues,
// each at aws_sqs_queue.<queue>[0], which dev's root generated afresh
// has at aws_sqs_queue.dev_<queue>. It returns a run into out through f,
// the merged root's directory and the members.
func remergeStack(t *testing.T, out string, f *fakeEngine, queues ...string) (*Run, string, []mergeMember) {
	t.Helper()
	run, stack, members := stackMembers(t, out, f, "dev", "prod")
	writeRootFiles(t, stack, map[string]string{"main.tf": "# merged\n"})
	ids := map[string]string{}
	for _, q := range queues {
		ids["aws_sqs_queue."+q+"[0]"] = "q-" + q
		members[0].result.Addresses = append(members[0].result.Addresses, engine.Managed{Type: "aws_sqs_queue", ID: "q-" + q, Address: "aws_sqs_queue.dev_" + q})
	}
	c := mergeCheckpoint{
		Members: []mergeCheckpointMember{{Env: "dev", Path: "dev/app"}, {Env: "prod", Path: "prod/app"}},
		Merge:   report.Merge{Path: "app", Merged: true, Environments: []string{"dev", "prod"}},
		Files:   []string{"main.tf"},
		Imports: map[string]map[string]string{"dev": ids},
	}
	if err := writeMergeCheckpoint(out, stack, c); err != nil {
		t.Fatal(err)
	}
	return run, stack, members
}

// readdressingMerge merges as the engine does a stack whose merged root
// remergeStack wrote, writing main.tf and queues.tf: of dev's queues, the
// first moving not kept apart go into a for_each, at other addresses
// than the merged root has them, and the rest stay where they were; with
// always, they go there kept apart or not. It records the roots of each
// merge in f.
func readdressingMerge(f *fakeEngine, queues []string, moving int, always bool) func(context.Context, string, []engine.MergeRoot, engine.MergeOptions) (*engine.MergeResult, error) {
	return func(_ context.Context, dir string, roots []engine.MergeRoot, _ engine.MergeOptions) (*engine.MergeResult, error) {
		f.merged = append(f.merged, slices.Clone(roots))
		dev := map[string]string{}
		moved := 0
		for _, q := range queues {
			apart := slices.Contains(roots[0].Isolated, "aws_sqs_queue.dev_"+q)
			if always || (!apart && moved < moving) {
				dev[`aws_sqs_queue.this["`+q+`"]`] = "q-" + q
				moved++
				continue
			}
			dev["aws_sqs_queue."+q+"[0]"] = "q-" + q
		}
		for name, content := range map[string]string{"main.tf": "# merged again\n", "queues.tf": "# queues\n"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				return nil, err
			}
		}
		return &engine.MergeResult{
			Files:        []string{"main.tf", "queues.tf"},
			Environments: []engine.MergedEnvironment{{Env: "dev", Imports: dev}, {Env: "prod"}},
		}, nil
	}
}

// checkMergedRootKept checks that the merged root in stack holds what it
// held before it merged again, and nothing the merge wrote.
func checkMergedRootKept(t *testing.T, stack string) {
	t.Helper()
	if content, err := os.ReadFile(filepath.Join(stack, "main.tf")); err != nil || string(content) != "# merged\n" {
		t.Errorf("the merged root isn't as it was: %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(stack, "queues.tf")); !os.IsNotExist(err) {
		t.Errorf("what the merge wrote is still in the merged root: %v", err)
	}
}

// A stack merged before merges again with what would get another address
// kept apart, as its environment's, under the label the merged root gives
// it, as many times as that takes, up to maxKeptApart; what would still
// get another address keeps the merged root as it was, and its roots set
// aside.
func TestMergeStackKeepsApartWhatWouldMove(t *testing.T) {
	for name, tc := range map[string]struct {
		queues []string
		moving int
		always bool
		// merges is how many times the stack merges, and isolated what
		// dev's root keeps apart the last time; kept says the merged root
		// stays as it was.
		merges   int
		isolated []string
		kept     bool
	}{
		"once kept apart":    {queues: []string{"audit"}, moving: 1, merges: 2, isolated: []string{"aws_sqs_queue.dev_audit"}},
		"all at once":        {queues: []string{"audit", "billing"}, moving: 2, merges: 2, isolated: []string{"aws_sqs_queue.dev_audit", "aws_sqs_queue.dev_billing"}},
		"nothing moves":      {queues: []string{"audit"}, merges: 1},
		"up to the limit":    {queues: []string{"a", "b", "c", "d"}, moving: 1, merges: 1 + maxKeptApart, isolated: []string{"aws_sqs_queue.dev_a", "aws_sqs_queue.dev_b", "aws_sqs_queue.dev_c"}, kept: true},
		"moves all the same": {queues: []string{"audit"}, always: true, merges: 2, isolated: []string{"aws_sqs_queue.dev_audit"}, kept: true},
	} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			f := &fakeEngine{}
			run, stack, members := remergeStack(t, out, f, tc.queues...)
			run.tools.merge = readdressingMerge(f, tc.queues, tc.moving, tc.always)
			if err := run.mergeStack(t.Context(), stack, members); err != nil {
				t.Fatal(err)
			}
			if len(f.merged) != tc.merges {
				t.Fatalf("merged %d times, want %d", len(f.merged), tc.merges)
			}
			last := f.merged[len(f.merged)-1]
			if !slices.Equal(last[0].Isolated, tc.isolated) || len(last[1].Isolated) != 0 {
				t.Errorf("kept apart %v and %v, want %v of dev's", last[0].Isolated, last[1].Isolated, tc.isolated)
			}
			for _, address := range tc.isolated {
				if want := strings.TrimPrefix(address, "aws_sqs_queue.dev_"); last[0].Labels[address] != want {
					t.Errorf("%s is labelled %q, want %q", address, last[0].Labels[address], want)
				}
			}
			m := run.report.Merges
			if len(m) != 1 || !m[0].Merged || m[0].Unchanged != tc.kept {
				t.Fatalf("merges %+v, want the merged root kept %v", m, tc.kept)
			}
			if !tc.kept {
				if content, err := os.ReadFile(filepath.Join(stack, "main.tf")); err != nil || string(content) != "# merged again\n" {
					t.Errorf("the merged root isn't merged again: %q, %v", content, err)
				}
				return
			}
			if !strings.Contains(m[0].Reason, "other addresses") {
				t.Errorf("merge %+v, want the merged root kept as it was", m[0])
			}
			checkMergedRootKept(t, stack)
			for _, member := range members {
				if set, err := premerged(out, member.dir); err != nil || !set {
					t.Errorf("%s's root isn't set aside: %v", member.env, err)
				}
			}
			if len(run.report.Failures) != 1 {
				t.Errorf("failures %v, want the merged root kept", run.report.Failures)
			}
		})
	}
}

// With --regenerate, what a stack merged before gives other addresses
// isn't kept apart: moved blocks follow it.
func TestMergeStackRegeneratingMovesWhatWouldMove(t *testing.T) {
	out := t.TempDir()
	f := &fakeEngine{}
	run, stack, members := remergeStack(t, out, f, "audit")
	run.options.Regenerate = true
	run.tools.merge = readdressingMerge(f, []string{"audit"}, 1, true)
	if err := run.mergeStack(t.Context(), stack, members); err != nil {
		t.Fatal(err)
	}
	if len(f.merged) != 1 || len(f.merged[0][0].Isolated) != 0 {
		t.Errorf("merged %d times, want once with nothing kept apart", len(f.merged))
	}
	want := []engine.Move{{From: "aws_sqs_queue.audit[0]", To: `aws_sqs_queue.this["audit"]`}}
	if got := f.mergedMoves["app"]; !reflect.DeepEqual(got, want) {
		t.Errorf("moved blocks %+v, want %+v", got, want)
	}
	if m := run.report.Merges; len(m) != 1 || !m[0].Merged {
		t.Errorf("merges %+v", m)
	}
}

// keepApart keeps apart, in each member and its root, what the merge
// would give another address, but what the merged root matched across
// environments, and reports whether it kept any apart.
func TestKeepApart(t *testing.T) {
	t.Parallel()
	members := []mergeMember{
		{env: "dev", result: &engine.Result{Addresses: []engine.Managed{
			{Type: "aws_sqs_queue", ID: "q-audit", Address: "aws_sqs_queue.dev_audit"},
			{Type: "aws_sqs_queue", ID: "q-orders", Address: "aws_sqs_queue.dev_orders"},
		}}},
		{env: "prod", isolated: []string{"aws_sqs_queue.prod_billing"}, labels: map[string]string{"aws_sqs_queue.prod_billing": "billing"}, result: &engine.Result{Addresses: []engine.Managed{
			{Type: "aws_sqs_queue", ID: "q-billing", Address: "aws_sqs_queue.prod_billing"},
			{Type: "aws_sqs_queue", ID: "q-reports", Address: "aws_sqs_queue.prod_reports"},
			{Type: "aws_sqs_queue", ID: "q-orders-prod", Address: "aws_sqs_queue.prod_orders"},
		}}},
	}
	prev := &mergeCheckpoint{Imports: map[string]map[string]string{
		"dev":  {"aws_sqs_queue.audit[0]": "q-audit", "aws_sqs_queue.orders": "q-orders"},
		"prod": {"aws_sqs_queue.billing[0]": "q-billing", "aws_sqs_queue.reports[0]": "q-reports", "aws_sqs_queue.orders": "q-orders-prod"},
	}}
	result := &engine.MergeResult{Environments: []engine.MergedEnvironment{
		{Env: "dev", Imports: map[string]string{`aws_sqs_queue.this["audit"]`: "q-audit", `aws_sqs_queue.this["orders"]`: "q-orders"}},
		{Env: "prod", Imports: map[string]string{`aws_sqs_queue.this["billing"]`: "q-billing", "aws_sqs_queue.reports[0]": "q-reports", `aws_sqs_queue.this["orders"]`: "q-orders-prod"}},
	}}
	roots := make([]engine.MergeRoot, len(members))
	s := &stackMerge{members: members, prev: prev}
	if !s.keepApart(roots, result, sharedAddresses(prev.Imports)) {
		t.Fatal("kept nothing apart")
	}
	// dev's audit moves, and its orders, matched with prod's, stays
	// merged; prod's billing is kept apart already, and its reports
	// doesn't move.
	want := []mergeMember{
		{env: "dev", isolated: []string{"aws_sqs_queue.dev_audit"}, labels: map[string]string{"aws_sqs_queue.dev_audit": "audit"}},
		{env: "prod", isolated: []string{"aws_sqs_queue.prod_billing"}, labels: map[string]string{"aws_sqs_queue.prod_billing": "billing"}},
	}
	for i, m := range s.members {
		if !slices.Equal(m.isolated, want[i].isolated) || !reflect.DeepEqual(m.labels, want[i].labels) {
			t.Errorf("%s keeps apart %v, %v; want %v, %v", m.env, m.isolated, m.labels, want[i].isolated, want[i].labels)
		}
	}
	if !slices.Equal(roots[0].Isolated, want[0].isolated) || !reflect.DeepEqual(roots[0].Labels, want[0].labels) {
		t.Errorf("dev's root keeps apart %v, %v", roots[0].Isolated, roots[0].Labels)
	}
	if roots[1].Isolated != nil || roots[1].Labels != nil {
		t.Errorf("prod's root, with nothing more kept apart, changed: %+v", roots[1])
	}
	// Merged again as it was, nothing more is kept apart.
	if s.keepApart(roots, result, sharedAddresses(prev.Imports)) {
		t.Errorf("kept more apart: %+v", s.members)
	}
}

// handOver lets a stack merged before merge again only where nothing it
// has would be left out, get another address or go to a root that
// didn't import it; else what the merge wrote goes, and the merge says
// why.
func TestHandOver(t *testing.T) {
	t.Parallel()
	before := map[string]map[string]string{"dev": {"aws_sqs_queue.audit": "q-audit", "aws_sqs_queue.orders": "q-orders"}}
	merged := &mergeCheckpoint{Merge: report.Merge{Merged: true}, Imports: before}
	for name, tc := range map[string]struct {
		prev    *mergeCheckpoint
		adopted bool
		reason  string
		imports map[string]map[string]string
		// why is what the merge's reason starts with, empty where it
		// merges, and cleared whether what it wrote goes.
		why     string
		cleared bool
	}{
		"never merged":   {imports: map[string]map[string]string{"dev": {"aws_sqs_queue.this": "q-audit"}}},
		"merged apart":   {prev: &mergeCheckpoint{Imports: before}, imports: map[string]map[string]string{"dev": {"aws_sqs_queue.this": "q-audit"}}},
		"doesn't merge":  {prev: merged, reason: "the plans differ", why: "the plans differ"},
		"as it was":      {prev: merged, imports: before},
		"left out":       {prev: merged, imports: map[string]map[string]string{"dev": {"aws_sqs_queue.audit": "q-audit"}}},
		"left out, told": {prev: merged, adopted: true, imports: map[string]map[string]string{"dev": {"aws_sqs_queue.audit": "q-audit"}}, why: "merging it again from what this run lists would leave out what the merged root has that this run doesn't list, but can't tell is no longer in the cloud (not listed in full with these services, filters and layout, or in another root): dev: aws_sqs_queue.orders", cleared: true},
		"readdressed":    {prev: merged, imports: map[string]map[string]string{"dev": {"aws_sqs_queue.audit": "q-audit", "aws_sqs_queue.this": "q-orders"}}, why: "merging it again would give what the merged root has other addresses: dev: aws_sqs_queue.orders is now aws_sqs_queue.this", cleared: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out := t.TempDir()
			stack := filepath.Join(out, "app")
			writeRootFiles(t, stack, map[string]string{"main.tf": "# merged\n"})
			run := NewRun()
			run.options.Out = out
			result := &engine.MergeResult{Reason: tc.reason, Files: []string{"main.tf"}}
			s := &stackMerge{run: run, out: out, stack: stack, prev: tc.prev, adopted: tc.adopted, result: result, imports: tc.imports}
			if err := s.handOver(); err != nil {
				t.Fatal(err)
			}
			if result.Reason != tc.why {
				t.Errorf("reason %q, want %q", result.Reason, tc.why)
			}
			if _, err := os.Stat(filepath.Join(stack, "main.tf")); os.IsNotExist(err) != tc.cleared {
				t.Errorf("what the merge wrote, cleared %v: %v", tc.cleared, err)
			}
		})
	}
}

// restorePrevious writes back what the merged root had, once only, even
// where writing it back fails.
func TestRestorePrevious(t *testing.T) {
	t.Parallel()
	stack := filepath.Join(t.TempDir(), "app")
	writeRootFiles(t, stack, map[string]string{"main.tf": "# merged\n"})
	b, err := backupTree(stack)
	if err != nil {
		t.Fatal(err)
	}
	s := &stackMerge{stack: stack, backup: b}
	if err := os.Remove(filepath.Join(stack, "main.tf")); err != nil {
		t.Fatal(err)
	}
	if err := s.restorePrevious(); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(stack, "main.tf")); err != nil || string(content) != "# merged\n" {
		t.Errorf("main.tf %q, %v", content, err)
	}
	if s.backup != nil {
		t.Error("the backup is still there")
	}
	writeRootFiles(t, stack, map[string]string{"main.tf": "# merged again\n"})
	if err := s.restorePrevious(); err != nil {
		t.Fatal(err)
	}
	if content, _ := os.ReadFile(filepath.Join(stack, "main.tf")); string(content) != "# merged again\n" {
		t.Errorf("restored twice: %q", content)
	}

	// The merged root's directory is a file now, so nothing can be
	// written back.
	s.backup = b
	if err := os.RemoveAll(stack); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stack, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.restorePrevious(); err == nil {
		t.Error("restoring into a file succeeded")
	}
	if s.backup != nil {
		t.Error("the backup is still there after failing")
	}
}
