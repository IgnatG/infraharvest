// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

func TestNeedsFreshGeneration(t *testing.T) {
	t.Parallel()
	failedPlan := gate.Gate{{Name: gate.CheckFormat, Passed: true}, {Name: gate.CheckPlan}}
	failedOther := gate.Gate{{Name: gate.CheckPlan, Passed: true}, {Name: gate.CheckScanners}}
	boom := errors.New("boom")
	for name, tc := range map[string]struct {
		result *Result
		err    error
		want   bool
	}{
		"no snapshot, a failed plan check": {&Result{Gate: failedPlan}, nil, false},
		"a snapshot, a failed plan check":  {&Result{UsedSnapshot: true, Gate: failedPlan}, nil, true},
		"a snapshot, another check failed": {&Result{UsedSnapshot: true, Gate: failedOther}, nil, false},
		"a snapshot, the gate passed":      {&Result{UsedSnapshot: true, Gate: gate.Gate{{Name: gate.CheckPlan, Passed: true}}}, nil, false},
		"a snapshot, an error":             {nil, &SnapshotError{Err: boom}, true},
		"an error without a snapshot":      {nil, boom, false},
		"a snapshot, stopped":              {nil, &SnapshotError{Err: context.Canceled}, false},
		"a snapshot, timed out":            {nil, &SnapshotError{Err: context.DeadlineExceeded}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := NeedsFreshGeneration(tc.result, tc.err); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
	if err := (&SnapshotError{Err: boom}); !errors.Is(err, boom) || err.Error() != "boom" {
		t.Errorf("SnapshotError: %v", err)
	}
}

func TestSnapshotRootFacts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"imports.tf": `
import {
  to = aws_s3_bucket.a
  id = "bucket-a"
}
import {
  to = module.m.aws_s3_bucket.main
  id = var.id
}
import {
  to = aws_iam_role.r
  identity = { name = "r" }
}
`,
		"generated.tf": `
resource "aws_s3_bucket" "a" {}
resource "aws_iam_role" "r" {}
data "aws_caller_identity" "me" {}
`,
		"x_override.tf": `resource "aws_s3_bucket" "a" {}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	blocks, err := importBlocks(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []tfrun.ImportBlock{{To: "aws_s3_bucket.a", ID: "bucket-a"}, {To: "module.m.aws_s3_bucket.main"}, {To: "aws_iam_role.r", ID: "name=r"}}
	if !slices.Equal(blocks, want) {
		t.Errorf("import blocks: got %+v, want %+v", blocks, want)
	}
	resources, err := resourceAddresses(dir)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(resources)
	if want := []string{"aws_iam_role.r", "aws_s3_bucket.a"}; !slices.Equal(resources, want) {
		t.Errorf("resources: got %v, want %v", resources, want)
	}
	if len(resources) != 2 {
		t.Errorf("an override or a data source was counted: %v", resources)
	}
}

// Without the option, or with a runner that can't run a snapshot's
// commands, the plans are the plain ones.
func TestCachedRunnerNeedsTheOption(t *testing.T) {
	t.Parallel()
	var plain tfrun.Terraform
	if c := cachedRunner(plain, plain, t.TempDir(), Options{}); c != nil {
		t.Error("a cache without the option")
	}
	opts := Options{ImportOnce: &ImportOnce{NewRunner: func(string) (tfrun.Terraform, error) { return nil, nil }}}
	if c := cachedRunner(plain, &fakeTerraform{}, t.TempDir(), opts); c != nil {
		t.Error("a cache for a runner that can't apply")
	}
	if (*tfrun.Cached)(nil).CachedPlans() != 0 {
		t.Error("a nil cache has plans")
	}
}

func TestDropBlocks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	main := "resource \"aws_s3_bucket\" \"a\" {\n  bucket = \"a\"\n}\n\nimport {\n  to = aws_s3_bucket.a\n  id = \"a\"\n}\n\nmoved {\n  from = aws_s3_bucket.old\n  to   = aws_s3_bucket.a\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dropBlocks(dir, "import", "moved"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "resource \"aws_s3_bucket\" \"a\" {\n  bucket = \"a\"\n}"; strings.TrimSpace(string(got)) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
