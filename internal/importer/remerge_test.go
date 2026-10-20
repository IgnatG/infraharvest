// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
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
