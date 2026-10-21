// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/report"
)

func TestStackLayout(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ pattern, want string }{
		{envLayout, "{out}/{provider}/{region}/"},
		{"{out}/{env}/{provider}/{account}/{region}/{service}/", "{out}/{provider}/{region}/{service}/"},
		{"{out}/{provider}/{account}-{env}/{region}", "{out}/{provider}/{region}"},
	} {
		if got := stackLayout(tc.pattern); got != tc.want {
			t.Errorf("stackLayout(%q) = %q, want %q", tc.pattern, got, tc.want)
		}
	}
}

func TestMergeRefusal(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	stack := filepath.Join(out, "aws", "eu-west-2")
	dev := mergeMember{env: "dev", dir: filepath.Join(out, "envs", "dev", "aws", "111111111111", "eu-west-2")}
	prod := mergeMember{env: "prod", dir: filepath.Join(out, "envs", "prod", "aws", "222222222222", "eu-west-2")}
	if reason := mergeRefusal(out, stack, []mergeMember{dev, prod}); reason != "" {
		t.Errorf("refused: %s", reason)
	}
	other := mergeMember{env: "prod", dir: filepath.Join(out, "envs", "prod", "aws", "333333333333", "eu-west-2")}
	if reason := mergeRefusal(out, stack, []mergeMember{dev, prod, other}); !strings.Contains(reason, "prod has 2 roots") {
		t.Errorf("two roots of prod: %q", reason)
	}
	nested := mergeMember{env: "prod", dir: filepath.Join(stack, "prod")}
	if reason := mergeRefusal(out, stack, []mergeMember{dev, nested}); !strings.Contains(reason, "in the merged root's directory") {
		t.Errorf("a root in the merged root's directory: %q", reason)
	}
	if err := os.MkdirAll(stack, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stack, "main.tf"), []byte("# hand-written\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if reason := mergeRefusal(out, stack, []mergeMember{dev, prod}); !strings.Contains(reason, "no merge by infraharvest wrote") {
		t.Errorf("a directory with configuration of its own: %q", reason)
	}
	if err := writeMergeCheckpoint(out, stack, mergeCheckpoint{Fingerprint: "x"}); err != nil {
		t.Fatal(err)
	}
	if reason := mergeRefusal(out, stack, []mergeMember{dev, prod}); reason != "" {
		t.Errorf("a merged root an earlier merge wrote: %q", reason)
	}
}

// A root with local state isn't merged: merging would set it aside, and
// its state would stay where no root is.
func TestMergeRefusalOfLocalState(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"terraform.tfstate", "terraform.tfstate.backup", "terraform.tfstate.d"} {
		t.Run(name, func(t *testing.T) {
			out := t.TempDir()
			stack := filepath.Join(out, "aws", "eu-west-2")
			dev := mergeMember{env: "dev", dir: filepath.Join(out, "envs", "dev", "aws", "111111111111", "eu-west-2")}
			prod := mergeMember{env: "prod", dir: filepath.Join(out, "envs", "prod", "aws", "222222222222", "eu-west-2")}
			writeRootFiles(t, dev.dir, map[string]string{"sqs.tf": "# queues\n"})
			if name == "terraform.tfstate.d" {
				writeRootFiles(t, filepath.Join(prod.dir, name, "prod"), map[string]string{"terraform.tfstate": "{}"})
			} else {
				writeRootFiles(t, prod.dir, map[string]string{name: "{}"})
			}

			reason := mergeRefusal(out, stack, []mergeMember{dev, prod})

			for _, want := range []string{"envs/prod/aws/222222222222/eu-west-2 has local state (" + name + ")", "migrate it to a backend first"} {
				if !strings.Contains(reason, want) {
					t.Errorf("reason %q misses %q", reason, want)
				}
			}
		})
	}
}

func TestCopyAndRemoveRoot(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	dir := filepath.Join(out, "envs", "dev", "aws", "111111111111", "eu-west-2")
	if err := os.MkdirAll(filepath.Join(dir, ".terraform", "providers"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sqs.tf", "imports.tf", ".terraform.lock.hcl", "README.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	premerge := premergeDir(out, dir)
	if want := filepath.Join(out, CheckpointDir, "premerge", "envs", "dev", "aws", "111111111111", "eu-west-2"); premerge != want {
		t.Errorf("premergeDir: %s, want %s", premerge, want)
	}
	if err := copyRoot(dir, premerge); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sqs.tf", "imports.tf", ".terraform.lock.hcl", "README.md"} {
		if _, err := os.Stat(filepath.Join(premerge, name)); err != nil {
			t.Errorf("%s isn't copied: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(premerge, ".terraform")); err == nil {
		t.Errorf("Terraform's working directory is copied")
	}
	if err := os.MkdirAll(filepath.Join(out, "envs", "prod"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeRoot(out, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "envs", "dev")); err == nil {
		t.Errorf("the empty directories above the root are left")
	}
	if _, err := os.Stat(filepath.Join(out, "envs", "prod")); err != nil {
		t.Errorf("another environment's directory is removed: %v", err)
	}
}

// Setting a root aside, and removing it, leave its local state where it
// is: never copied, never removed.
func TestCopyAndRemoveRootLeaveLocalState(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	dir := filepath.Join(out, "envs", "dev", "aws", "111111111111", "eu-west-2")
	writeRootFiles(t, dir, map[string]string{"sqs.tf": "# queues\n", "terraform.tfstate": "state", "terraform.tfstate.backup": "backup"})
	writeRootFiles(t, filepath.Join(dir, "terraform.tfstate.d", "dev"), map[string]string{"terraform.tfstate": "dev"})
	premerge := premergeDir(out, dir)
	if err := copyRoot(dir, premerge); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"terraform.tfstate", "terraform.tfstate.backup", "terraform.tfstate.d"} {
		if _, err := os.Stat(filepath.Join(premerge, name)); err == nil {
			t.Errorf("%s is copied", name)
		}
	}
	if err := removeRoot(out, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sqs.tf")); err == nil {
		t.Errorf("sqs.tf isn't removed")
	}
	for name, want := range map[string]string{"terraform.tfstate": "state", "terraform.tfstate.backup": "backup", filepath.Join("terraform.tfstate.d", "dev", "terraform.tfstate"): "dev"} {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != want {
			t.Errorf("%s: %q, %v, want %q", name, got, err, want)
		}
	}
}

func TestRecordMerge(t *testing.T) {
	t.Parallel()
	run := NewRun()
	run.options.Out = "out"
	run.report.Directories = []report.Directory{{Path: "envs/dev/aws/111111111111/eu-west-2"}, {Path: "envs/prod/aws/222222222222/eu-west-2"}, {Path: "aws/333333333333/eu-west-2"}}
	members := []mergeMember{{env: "dev", dir: filepath.Join("out", "envs", "dev", "aws", "111111111111", "eu-west-2")}, {env: "prod", dir: filepath.Join("out", "envs", "prod", "aws", "222222222222", "eu-west-2")}}
	run.recordMerge(report.Merge{Path: "aws/eu-west-2", Merged: true}, members)
	for i, want := range []string{"aws/eu-west-2", "aws/eu-west-2", ""} {
		if got := run.report.Directories[i].MergedInto; got != want {
			t.Errorf("%s: merged into %q, want %q", run.report.Directories[i].Path, got, want)
		}
	}
	if len(run.report.Merges) != 1 {
		t.Errorf("merges: %+v", run.report.Merges)
	}
}
