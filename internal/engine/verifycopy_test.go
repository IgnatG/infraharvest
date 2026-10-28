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

	"github.com/IgnatG/infraharvest/internal/engine/gate"
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
	var inspector *copyInspector
	newTF := func(d string) (tfrun.Terraform, error) {
		inspector = &copyInspector{Terraform: &fakeTerraform{dir: d}, dir: d}
		return inspector, nil
	}
	if _, err := Verify(context.Background(), newTF, dir, Options{}); err != nil {
		t.Fatal(err)
	}
	if inspector == nil {
		t.Fatal("no runner for the copy")
	}
	if filepath.Dir(inspector.dir) != filepath.Dir(dir) || !gate.IsVerifyCopy(filepath.Base(inspector.dir)) {
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
