// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// writeRootFiles writes a root with files, by name, into dir.
func writeRootFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPremerged(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	dir := filepath.Join(out, "envs", "dev", "aws", "111111111111", "eu-west-2")
	writeRootFiles(t, dir, map[string]string{engine.VersionsFileName: "# versions\n", "sqs.tf": "# queues\n"})
	if set, err := premerged(out, dir); err != nil || set {
		t.Errorf("a root in place: %v %v", set, err)
	}
	if got := rootSource(out, dir); got != dir {
		t.Errorf("rootSource of a root in place: %s", got)
	}
	if err := setAside(out, dir); err != nil {
		t.Fatal(err)
	}
	if set, err := premerged(out, dir); err != nil || !set {
		t.Errorf("a root set aside: %v %v", set, err)
	}
	if got := rootSource(out, dir); got != premergeDir(out, dir) {
		t.Errorf("rootSource of a root set aside: %s", got)
	}
}

func TestReaddressed(t *testing.T) {
	t.Parallel()
	before := map[string]map[string]string{"dev": {"aws_sqs_queue.orders": "q1", "aws_sqs_queue.audit[0]": "q2"}}
	same := map[string]map[string]string{"dev": {"aws_sqs_queue.orders": "q1", "aws_sqs_queue.audit[0]": "q2", "aws_sqs_queue.new_dev[0]": "q3"}}
	if moved := readdressed(before, same); len(moved) != 0 {
		t.Errorf("nothing moved: %v", moved)
	}
	moved := readdressed(before, map[string]map[string]string{"dev": {"aws_sqs_queue.orders": "q1", `aws_sqs_queue.this["audit"]`: "q2"}})
	if len(moved) != 1 || !strings.Contains(moved[0], `aws_sqs_queue.audit[0] is now aws_sqs_queue.this["audit"]`) {
		t.Errorf("moved: %v", moved)
	}
}

// With --regenerate, what a merged root has at other addresses once it
// merges again moves there, after the moved blocks of its moves.tf from
// before; an address that would move to two, or one in a module call,
// can't.
func TestMergedMoves(t *testing.T) {
	t.Parallel()
	before := map[string]map[string]string{
		"dev":  {"aws_sqs_queue.orders": "q1", "aws_sqs_queue.audit": "q2"},
		"prod": {"aws_sqs_queue.orders": "q3"},
	}
	now := map[string]map[string]string{
		"dev":  {`aws_sqs_queue.this["orders"]`: "q1", `aws_sqs_queue.this["audit"]`: "q2"},
		"prod": {`aws_sqs_queue.this["orders"]`: "q3"},
	}
	kept := []engine.Move{{From: "aws_sqs_queue.orders_old", To: "aws_sqs_queue.orders"}}
	moves, why := mergedMoves(before, now, kept)
	want := []engine.Move{kept[0], {From: "aws_sqs_queue.audit", To: `aws_sqs_queue.this["audit"]`}, {From: "aws_sqs_queue.orders", To: `aws_sqs_queue.this["orders"]`}}
	if why != "" || !slices.Equal(moves, want) {
		t.Errorf("moves %+v (%s), want %+v", moves, why, want)
	}
	if moves, why := mergedMoves(before, before, kept); why != "" || !slices.Equal(moves, kept) {
		t.Errorf("nothing moved: %+v (%s), want the kept ones", moves, why)
	}
	twice := map[string]map[string]string{
		"dev":  now["dev"],
		"prod": {`aws_sqs_queue.this["prod_orders"]`: "q3"},
	}
	if _, why := mergedMoves(before, twice, nil); !strings.Contains(why, "both") {
		t.Errorf("an address moving to two: %q", why)
	}
	module := map[string]map[string]string{"dev": {"module.queues.aws_sqs_queue.this": "q1"}}
	if _, why := mergedMoves(map[string]map[string]string{"dev": {"module.old.aws_sqs_queue.this": "q1"}}, module, nil); !strings.Contains(why, "module calls") {
		t.Errorf("a resource of a module call: %q", why)
	}

	// Without --regenerate, the merged root stays as it was.
	r := NewRun()
	if why, err := r.moveMerged(t.TempDir(), &engine.MergeResult{}, before, now, nil); err != nil || !strings.Contains(why, "other addresses") {
		t.Errorf("without --regenerate: %q, %v", why, err)
	}
}

func TestKeptAsMerged(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	stack := filepath.Join(out, "aws", "eu-west-2")
	writeRootFiles(t, stack, map[string]string{"providers.tf": "# providers\n"})
	merged := &mergeCheckpoint{Merge: report.Merge{Merged: true}, Files: []string{"providers.tf"}}
	members := []mergeMember{{env: "dev", premerged: true}, {env: "prod", premerged: true}}
	if !keptAsMerged(stack, merged, members) {
		t.Errorf("a merged root with its files and its roots set aside")
	}
	if keptAsMerged(stack, &mergeCheckpoint{Merge: report.Merge{Merged: true}, Files: []string{"providers.tf", "sqs.tf"}}, members) {
		t.Errorf("a merged root that lost a file")
	}
	if keptAsMerged(stack, merged, []mergeMember{{env: "dev", premerged: true}, {env: "prod"}}) {
		t.Errorf("a root generated again beside its merged root")
	}
	if !keptAsMerged(stack, &mergeCheckpoint{}, []mergeMember{{env: "dev"}, {env: "prod"}}) {
		t.Errorf("roots kept apart, in place")
	}
	if got := missingEnvironments(&mergeCheckpoint{Members: []mergeCheckpointMember{{Env: "dev"}, {Env: "prod"}}}, []string{"dev"}); strings.Join(got, ",") != "prod" {
		t.Errorf("missing environments: %v", got)
	}
}

func TestBackupTree(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
	writeRootFiles(t, filepath.Join(dir, "vars", "dev"), map[string]string{"terraform.tfvars": "env = \"dev\"\n"})
	writeRootFiles(t, dir, map[string]string{"main.tf": "# main\n"})
	writeRootFiles(t, filepath.Join(dir, ".terraform"), map[string]string{"x": "x"})
	backup, err := backupTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := backup.restore(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main.tf", filepath.Join("vars", "dev", "terraform.tfvars")} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s isn't restored: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".terraform")); err == nil {
		t.Errorf("Terraform's working directory is restored")
	}
	if err := (*treeBackup)(nil).restore(); err != nil {
		t.Errorf("a nil backup: %v", err)
	}
}

// A merged root's backup leaves out its local state, such as its
// workspaces', so that restoring it never writes back a state Terraform
// may have written since.
func TestBackupTreeLeavesOutLocalState(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
	writeRootFiles(t, dir, map[string]string{"main.tf": "# main\n", "terraform.tfstate": "before"})
	writeRootFiles(t, filepath.Join(dir, "terraform.tfstate.d", "dev"), map[string]string{"terraform.tfstate": "before"})
	backup, err := backupTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeRootFiles(t, dir, map[string]string{"terraform.tfstate": "after"})
	writeRootFiles(t, filepath.Join(dir, "terraform.tfstate.d", "dev"), map[string]string{"terraform.tfstate": "after"})
	if err := backup.restore(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"terraform.tfstate", filepath.Join("terraform.tfstate.d", "dev", "terraform.tfstate")} {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != "after" {
			t.Errorf("%s: %q, %v, want it as Terraform last wrote it", name, got, err)
		}
	}
}

func TestMergedHolds(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	stack := filepath.Join(out, "aws", "eu-west-2")
	writeRootFiles(t, stack, map[string]string{"providers.tf": "# providers\n"})
	c := mergeCheckpoint{
		Merge:   report.Merge{Path: "aws/eu-west-2", Merged: true},
		Members: []mergeCheckpointMember{{Env: "dev", Path: "envs/dev/aws/111111111111/eu-west-2"}},
		Imports: map[string]map[string]string{"dev": {`aws_sqs_queue.this["orders"]`: "q1"}},
	}
	if err := writeMergeCheckpoint(out, stack, c); err != nil {
		t.Fatal(err)
	}
	held, err := mergedHolds(out, map[string][]engine.Import{filepath.Join(out, "aws", "333333333333", "eu-west-2"): nil})
	if err != nil {
		t.Fatal(err)
	}
	if got := held[engine.External{Type: "aws_sqs_queue", ID: "q1"}]; got != "aws/eu-west-2" {
		t.Errorf("held by %q", got)
	}
	// A run that imports one of its roots adds to it.
	held, err = mergedHolds(out, map[string][]engine.Import{filepath.Join(out, "envs", "dev", "aws", "111111111111", "eu-west-2"): nil})
	if err != nil || len(held) != 0 {
		t.Errorf("held from its own run: %v %v", held, err)
	}
}

func TestReaddressNotInCloud(t *testing.T) {
	t.Parallel()
	run := NewRun()
	run.options.Out = "out"
	run.report.NotInCloud = []report.NotInCloud{{Path: "envs/dev/aws/111111111111/eu-west-2", Address: "aws_sqs_queue.orders", ID: "q1"}}
	members := []mergeMember{{env: "dev", dir: filepath.Join("out", "envs", "dev", "aws", "111111111111", "eu-west-2")}}
	result := &engine.MergeResult{Environments: []engine.MergedEnvironment{{Env: "dev", Addresses: map[string]string{"aws_sqs_queue.orders": `aws_sqs_queue.this["orders"]`}}}}
	run.readdressNotInCloud(report.Merge{Path: "aws/eu-west-2", Merged: true}, members, result)
	if g := run.report.NotInCloud[0]; g.Path != "aws/eu-west-2" || g.Address != `aws_sqs_queue.this["orders"]` {
		t.Errorf("not in cloud: %+v", g)
	}
}

// joiningStack is a stack merged before of dev and prod, which a run
// brings staging to: prev, its checkpoint, and the members, dev and
// prod's restored from their roots set aside, and staging's root in
// place, which the merge copied aside as it does before merging.
func joiningStack(t *testing.T, out string) (*mergeCheckpoint, []mergeMember, string) {
	t.Helper()
	prev := &mergeCheckpoint{
		Merge:   report.Merge{Path: "aws/eu-west-2", Merged: true, Environments: []string{"dev", "prod"}},
		Members: []mergeCheckpointMember{{Env: "dev", Path: "envs/dev/aws/111111111111/eu-west-2"}, {Env: "prod", Path: "envs/prod/aws/222222222222/eu-west-2"}},
	}
	staging := filepath.Join(out, "envs", "staging", "aws", "333333333333", "eu-west-2")
	writeRootFiles(t, staging, map[string]string{engine.VersionsFileName: "# versions\n", "sqs.tf": "# queues\n"})
	if err := copyRoot(staging, premergeDir(out, staging)); err != nil {
		t.Fatal(err)
	}
	members := []mergeMember{
		{env: "dev", dir: filepath.Join(out, "envs", "dev", "aws", "111111111111", "eu-west-2"), premerged: true, restored: true},
		{env: "prod", dir: filepath.Join(out, "envs", "prod", "aws", "222222222222", "eu-west-2"), premerged: true, restored: true},
		{env: "staging", dir: staging},
	}
	return prev, members, staging
}

// checkApart checks that run recorded the merge of aws/eu-west-2 kept as
// it was, with staging's root, in place under out, apart, as why says.
func checkApart(t *testing.T, run *Run, out, staging, why string) {
	t.Helper()
	if len(run.report.Merges) != 1 {
		t.Fatalf("merges: %+v", run.report.Merges)
	}
	m := run.report.Merges[0]
	if !m.Merged || !m.Unchanged || m.ApartReason != why || len(m.Apart) != 1 || m.Apart[0] != (report.MergedRoot{Env: "staging", Path: "envs/staging/aws/333333333333/eu-west-2"}) {
		t.Errorf("merge: %+v", m)
	}
	if _, err := os.Stat(filepath.Join(staging, "sqs.tf")); err != nil {
		t.Errorf("staging's root isn't in place: %v", err)
	}
	if _, err := os.Stat(premergeDir(out, staging)); err == nil {
		t.Errorf("staging's root is set aside")
	}
}

func TestJoiningEnvironments(t *testing.T) {
	t.Parallel()
	prev, members, _ := joiningStack(t, t.TempDir())
	if got := joiningEnvironments(prev, members); strings.Join(got, ",") != "staging" {
		t.Errorf("joining: %v", got)
	}
	if got := joiningEnvironments(&mergeCheckpoint{Members: prev.Members}, members); len(got) != 0 {
		t.Errorf("joining a stack kept apart: %v", got)
	}
	if got := joiningEnvironments(nil, members); len(got) != 0 {
		t.Errorf("joining a stack never merged: %v", got)
	}
	rest, apart := withoutEnvironments(members, []string{"staging"})
	if len(rest) != 2 || len(apart) != 1 || apart[0].env != "staging" {
		t.Errorf("without staging: %+v, %+v", rest, apart)
	}
}

// TestMergeWithout leaves staging, which couldn't join the merged root,
// apart: with nothing new of dev and prod, the merged root stays as it
// was, and the report says why staging keeps its root.
func TestMergeWithout(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	prev, members, staging := joiningStack(t, out)
	run := NewRun()
	run.options.Out = out
	why := "merging it again would give what the merged root has other addresses: dev: aws_sqs_queue.orders is now aws_sqs_queue.orders_dev[0]"
	if err := run.mergeWithout(context.Background(), filepath.Join(out, "aws", "eu-west-2"), prev, members, []string{"staging"}, why); err != nil {
		t.Fatal(err)
	}
	checkApart(t, run, out, staging, why)
	if len(run.report.Failures) != 0 {
		t.Errorf("failures: %v", run.report.Failures)
	}
}

// TestKeepMergedJoining keeps the merged root as it was, and staging,
// new to it, keeps its root in place rather than set aside.
func TestKeepMergedJoining(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	prev, members, staging := joiningStack(t, out)
	if err := os.RemoveAll(premergeDir(out, staging)); err != nil {
		t.Fatal(err)
	}
	run := NewRun()
	run.options.Out = out
	if err := run.keepMerged(filepath.Join(out, "aws", "eu-west-2"), prev, members, "why"); err != nil {
		t.Fatal(err)
	}
	checkApart(t, run, out, staging, "why")
	if len(run.report.Failures) != 1 {
		t.Errorf("failures: %v", run.report.Failures)
	}
}
