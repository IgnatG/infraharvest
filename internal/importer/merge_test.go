// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/envmerge"
	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/harvest"
	awsprovider "github.com/IgnatG/infraharvest/internal/providers/aws"
	azureprovider "github.com/IgnatG/infraharvest/internal/providers/azure"
	googleprovider "github.com/IgnatG/infraharvest/internal/providers/google"
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
	if reason := mergeRefusal(out, stack, []mergeMember{dev, prod}, false); reason != "" {
		t.Errorf("refused: %s", reason)
	}
	other := mergeMember{env: "prod", dir: filepath.Join(out, "envs", "prod", "aws", "333333333333", "eu-west-2")}
	if reason := mergeRefusal(out, stack, []mergeMember{dev, prod, other}, false); !strings.Contains(reason, "prod has 2 roots") {
		t.Errorf("two roots of prod: %q", reason)
	}
	nested := mergeMember{env: "prod", dir: filepath.Join(stack, "prod")}
	if reason := mergeRefusal(out, stack, []mergeMember{dev, nested}, false); !strings.Contains(reason, "in the merged root's directory") {
		t.Errorf("a root in the merged root's directory: %q", reason)
	}
	if err := os.MkdirAll(stack, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stack, "main.tf"), []byte("# hand-written\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if reason := mergeRefusal(out, stack, []mergeMember{dev, prod}, false); !strings.Contains(reason, "no merge by infraharvest wrote") {
		t.Errorf("a directory with configuration of its own: %q", reason)
	}
	if reason := mergeRefusal(out, stack, []mergeMember{dev, prod}, true); reason != "" {
		t.Errorf("a merged root told from its files: %q", reason)
	}
	if err := writeMergeCheckpoint(out, stack, mergeCheckpoint{Fingerprint: "x"}); err != nil {
		t.Fatal(err)
	}
	if reason := mergeRefusal(out, stack, []mergeMember{dev, prod}, false); reason != "" {
		t.Errorf("a merged root an earlier merge wrote: %q", reason)
	}
	// A checkpoint that can't be read isn't taken for none, even for a
	// merged root told from its files.
	if err := os.WriteFile(mergeCheckpointPath(out, stack), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, adopted := range []bool{false, true} {
		if reason := mergeRefusal(out, stack, []mergeMember{dev, prod}, adopted); !strings.Contains(reason, "merge checkpoint can't be read") || !strings.Contains(reason, mergeCheckpointPath(out, stack)) {
			t.Errorf("a corrupt checkpoint, adopted %v: %q", adopted, reason)
		}
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

			reason := mergeRefusal(out, stack, []mergeMember{dev, prod}, false)

			for _, want := range []string{"envs/prod/aws/222222222222/eu-west-2 has local state (" + name + ")", "migrate it to a backend first"} {
				if !strings.Contains(reason, want) {
					t.Errorf("reason %q misses %q", reason, want)
				}
			}
		})
	}
}

// With --cfn-stack, only the roots their stack's merge is to take are
// marked merging, and take no stack template: not those of a stack with
// one environment in the import, unless it merged before, nor those
// mergeRefusal refuses.
func TestMarkMerging(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	stack := filepath.Join(out, "aws", "eu-west-2")
	devDir := filepath.Join(out, "envs", "dev", "aws", "eu-west-2")
	prodDir := filepath.Join(out, "envs", "prod", "aws", "eu-west-2")
	options := Options{Out: out, MergeEnvironments: true, ProviderScope: fakeScope{stacks: true}}
	calls := func(envs ...string) []*listedCall {
		c := &listedCall{places: map[string]memberPlace{}}
		for _, env := range envs {
			c.places[filepath.Join(out, "envs", env, "aws", "eu-west-2")] = memberPlace{env: env, stack: stack}
		}
		return []*listedCall{c, nil}
	}

	both := calls("dev", "prod")
	if err := markMerging(options, both); err != nil {
		t.Fatal(err)
	}
	if !both[0].merging[devDir] || !both[0].merging[prodDir] {
		t.Errorf("dev and prod: merging = %v", both[0].merging)
	}

	// Only prod in scope: the stack doesn't merge, and its root keeps the
	// template.
	prod := calls("prod")
	if err := markMerging(options, prod); err != nil {
		t.Fatal(err)
	}
	if len(prod[0].merging) != 0 {
		t.Errorf("prod alone: merging = %v", prod[0].merging)
	}

	// Without --cfn-stack, or --merge-environments, nothing asks.
	for _, o := range []Options{{Out: out, MergeEnvironments: true}, {Out: out, ProviderScope: fakeScope{stacks: true}}} {
		cs := calls("dev", "prod")
		if err := markMerging(o, cs); err != nil {
			t.Fatal(err)
		}
		if len(cs[0].merging) != 0 {
			t.Errorf("%+v: merging = %v", o, cs[0].merging)
		}
	}

	// A directory with configuration no merge wrote: refused.
	writeRootFiles(t, stack, map[string]string{"main.tf": "# hand-written\n"})
	refused := calls("dev", "prod")
	if err := markMerging(options, refused); err != nil {
		t.Fatal(err)
	}
	if len(refused[0].merging) != 0 {
		t.Errorf("refused: merging = %v", refused[0].merging)
	}

	// Merged before with dev: prod alone merges again with it.
	if err := writeMergeCheckpoint(out, stack, mergeCheckpoint{Fingerprint: "x", Merge: report.Merge{Merged: true, Environments: []string{"dev", "prod"}}}); err != nil {
		t.Fatal(err)
	}
	again := calls("prod")
	if err := markMerging(options, again); err != nil {
		t.Fatal(err)
	}
	if !again[0].merging[prodDir] {
		t.Errorf("merged before: merging = %v", again[0].merging)
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

// A merged root's findings are each environment's, about the resources
// it imports by their addresses in the merged root; a merge that didn't
// happen has none, as its root is gone.
func TestFillMergeFindings(t *testing.T) {
	t.Parallel()
	checks := gate.Gate{{Name: gate.CheckScanners, Passed: true, Findings: []gate.Finding{
		{Scanner: "conftest", Rule: "s3", Severity: "failure", Location: "main.tf", Message: "logs has no owner tag", Resource: "aws_s3_bucket.logs"},
	}}}
	result := &envmerge.MergeResult{Environments: []envmerge.MergedEnvironment{
		{Env: "dev", Gate: checks, Imports: map[string]string{"aws_s3_bucket.logs": "acme-dev-logs"}},
		{Env: "prod", Gate: checks, Imports: map[string]string{"module.logs.aws_s3_bucket.this": "acme-prod-logs"}},
	}}
	merge := report.Merge{Path: "aws/eu-west-2"}
	fillMerge(&merge, result)
	want := []report.Finding{
		{Root: "aws/eu-west-2", Environment: "dev", Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", ID: "acme-dev-logs", Scanner: "conftest", Rule: "s3", Severity: "failure", Location: "main.tf", Message: "logs has no owner tag"},
		{Root: "aws/eu-west-2", Environment: "prod", Address: "aws_s3_bucket.logs", Scanner: "conftest", Rule: "s3", Severity: "failure", Location: "main.tf", Message: "logs has no owner tag"},
	}
	if !reflect.DeepEqual(merge.Findings, want) {
		t.Errorf("got %+v,\nwant %+v", merge.Findings, want)
	}
	result.Reason = "too different"
	refused := report.Merge{Path: "aws/eu-west-2"}
	fillMerge(&refused, result)
	if refused.Findings != nil {
		t.Errorf("a merge that didn't happen: got %+v", refused.Findings)
	}
}

// A merged root's README titles the scope by its noun, and reads the
// rest of the provider's Merge as it is.
func TestMergeProfile(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		provider harvest.Provider
		title    string
	}{
		{&awsprovider.Provider{}, "Account"},
		{&azureprovider.Provider{}, "Subscription"},
		{&googleprovider.Provider{}, "Project"},
	} {
		got := mergeProfile(tc.provider.Profile())
		merge := tc.provider.Profile().Merge
		if got.ScopeTitle != tc.title || got.ScopeExpr != merge.ScopeExpr || got.RegionExpr != merge.RegionExpr || got.RegionTitle != merge.RegionTitle {
			t.Errorf("%s: %+v", tc.provider.Name(), got)
		}
	}
}
