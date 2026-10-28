// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package envmerge

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/engine/rootfile"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// copyInspector is a Terraform runner that records, when initialised,
// the files of the directory it runs in, by path with forward slashes.
type copyInspector struct {
	tfrun.Terraform
	dir  string
	seen map[string]string
}

func (c *copyInspector) Init(ctx context.Context, opts ...tfexec.InitOption) error {
	c.seen = map[string]string{}
	err := filepath.WalkDir(c.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(c.dir, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		c.seen[filepath.ToSlash(rel)] = string(content)
		return err
	})
	if err != nil {
		return err
	}
	return c.Terraform.Init(ctx, opts...)
}

// VerifyMerged plans a merged root with a backend in a copy without it,
// with the environment's var files of the copy.
func TestVerifyMergedChecksACopyWithoutTheBackend(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
	var overrides []string
	roots := mergeRoots(t, dir, nil, &overrides)
	for i, account := range []string{"111111111111", "222222222222"} {
		backend := "terraform {\n  backend \"s3\" {\n    bucket = \"state\"\n    key    = \"imported/aws/" + account + "/eu-west-2/terraform.tfstate\"\n    region = \"eu-west-2\"\n  }\n}\n"
		if err := os.WriteFile(filepath.Join(roots[i].Dir, "backend.tf"), []byte(backend), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := MergeRoots(context.Background(), dir, roots, MergeOptions{Profile: mergeProfiles["aws"], Gate: testGate}); err != nil || result.Reason != "" {
		t.Fatalf("merge: %v %+v", err, result)
	}
	providers := readFile(t, dir, rootfile.ProvidersFileName)
	if !strings.Contains(providers, "backend \"s3\"") {
		t.Fatalf("the merged root has no backend:\n%s", providers)
	}
	var inspector *copyInspector
	newTF := func(d string) (tfrun.Terraform, error) {
		inspector = &copyInspector{Terraform: &mergeFake{env: "prod", dir: d, overrides: &overrides}, dir: d}
		return inspector, nil
	}
	checks, err := VerifyMerged(context.Background(), newTF, dir, "prod", MergeOptions{Gate: testGate})
	if err != nil {
		t.Fatal(err)
	}
	if !checks.Passed() {
		t.Errorf("gate: %+v", checks)
	}
	if got := inspector.seen[rootfile.ProvidersFileName]; strings.Contains(got, "backend \"") {
		t.Errorf("the copy's providers.tf has the backend:\n%s", got)
	}
	if _, ok := inspector.seen["vars/prod/backend.hcl"]; !ok {
		t.Errorf("the copy lacks the var files: %v", inspector.seen)
	}
	if got := readFile(t, dir, rootfile.ProvidersFileName); got != providers {
		t.Errorf("providers.tf changed:\n%s", got)
	}
}
