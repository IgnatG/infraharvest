// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-exec/tfexec"

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

// verifiedRoot writes a root with a backend, in providers.tf as a merged
// root has it and in backend.tf as another root does, with state, and
// returns it and its files, by path with forward slashes.
func verifiedRoot(t *testing.T) (string, map[string]string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "aws", "eu-west-2")
	files := map[string]string{
		"providers.tf":               "terraform {\n  required_version = \">= 1.5\"\n\n  backend \"s3\" {}\n}\n\nprovider \"aws\" {\n  region = \"eu-west-2\"\n}\n",
		"backend.tf":                 "terraform {\n  backend \"s3\" {\n    bucket = \"state\"\n  }\n}\n",
		"main.tf":                    "resource \"aws_sqs_queue\" \"a\" {\n  name = \"a\"\n}\n",
		"vars/prod/terraform.tfvars": "env = \"prod\"\n",
		"terraform.tfstate":          "{}",
		"terraform.tfstate.d/prod/terraform.tfstate": "{}",
		".terraform/terraform.tfstate":               "{\"backend\":{\"type\":\"s3\"}}",
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, files
}

// Verify checks a root in a private copy beside it, without its backend,
// its state or Terraform's working directory, and leaves the root as it
// was.
func TestVerifyChecksACopyWithoutTheBackend(t *testing.T) {
	t.Parallel()
	dir, files := verifiedRoot(t)
	var calls []string
	var inspector *copyInspector
	newTF := func(d string) (tfrun.Terraform, error) {
		inspector = &copyInspector{Terraform: &fakeDriftTerraform{name: "verify", calls: &calls}, dir: d}
		return inspector, nil
	}
	if _, err := Verify(context.Background(), newTF, dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if inspector == nil {
		t.Fatal("no runner for the copy")
	}
	if filepath.Dir(inspector.dir) != filepath.Dir(dir) || !IsVerifyCopy(filepath.Base(inspector.dir)) {
		t.Errorf("the copy is %s, want a hidden directory beside %s", inspector.dir, dir)
	}
	for _, name := range []string{"providers.tf", "backend.tf"} {
		if got, ok := inspector.seen[name]; !ok || strings.Contains(got, "backend \"") {
			t.Errorf("the copy's %s: %q, want it without its backend", name, got)
		}
	}
	if got := inspector.seen["providers.tf"]; got != string(hclwrite.Format([]byte(got))) || !strings.Contains(got, "provider \"aws\"") {
		t.Errorf("the copy's providers.tf isn't formatted as the root's is:\n%s", got)
	}
	for _, name := range []string{"main.tf", "vars/prod/terraform.tfvars"} {
		if inspector.seen[name] != files[name] {
			t.Errorf("the copy's %s: %q, want %q", name, inspector.seen[name], files[name])
		}
	}
	for _, name := range []string{"terraform.tfstate", "terraform.tfstate.d/prod/terraform.tfstate", ".terraform/terraform.tfstate"} {
		if _, ok := inspector.seen[name]; ok {
			t.Errorf("the copy has %s", name)
		}
	}
	if _, err := os.Stat(inspector.dir); !os.IsNotExist(err) {
		t.Errorf("the copy is left: %v", err)
	}
	for name, want := range files {
		if got := readFile(t, dir, filepath.FromSlash(name)); got != want {
			t.Errorf("%s: %q, want it as it was", name, got)
		}
	}
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
	if result, err := MergeRoots(context.Background(), dir, roots, MergeOptions{Profile: mergeProfiles["aws"]}); err != nil || result.Reason != "" {
		t.Fatalf("merge: %v %+v", err, result)
	}
	providers := readFile(t, dir, ProvidersFileName)
	if !strings.Contains(providers, "backend \"s3\"") {
		t.Fatalf("the merged root has no backend:\n%s", providers)
	}
	var inspector *copyInspector
	newTF := func(d string) (tfrun.Terraform, error) {
		inspector = &copyInspector{Terraform: &mergeFake{env: "prod", dir: d, overrides: &overrides}, dir: d}
		return inspector, nil
	}
	gate, err := VerifyMerged(context.Background(), newTF, dir, "prod", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !gate.Passed() {
		t.Errorf("gate: %+v", gate)
	}
	if got := inspector.seen[ProvidersFileName]; strings.Contains(got, "backend \"") {
		t.Errorf("the copy's providers.tf has the backend:\n%s", got)
	}
	if _, ok := inspector.seen["vars/prod/backend.hcl"]; !ok {
		t.Errorf("the copy lacks the var files: %v", inspector.seen)
	}
	if got := readFile(t, dir, ProvidersFileName); got != providers {
		t.Errorf("providers.tf changed:\n%s", got)
	}
}

// dropBackend keeps a file formatted as it was: formatted where it was,
// else as it is but for the block it removes.
func TestDropBackend(t *testing.T) {
	t.Parallel()
	formatted := "terraform {\n  required_version = \">= 1.5\"\n\n  backend \"s3\" {}\n}\n"
	if got := string(dropBackend([]byte(formatted))); strings.Contains(got, "backend") || got != string(hclwrite.Format([]byte(got))) {
		t.Errorf("formatted: %q", got)
	}
	unformatted := "terraform {\n  required_version=\">= 1.5\"\n  backend \"s3\" {}\n}\n"
	if got := string(dropBackend([]byte(unformatted))); strings.Contains(got, "backend") || !strings.Contains(got, "required_version=") {
		t.Errorf("unformatted: %q", got)
	}
	none := "provider \"aws\" {}\n"
	if got := string(dropBackend([]byte(none))); got != none {
		t.Errorf("without a backend: %q", got)
	}
}
