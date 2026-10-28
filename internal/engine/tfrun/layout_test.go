// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"path/filepath"
	"strings"
	"testing"
)

// A root without a backend keeps each environment's state in the
// workspace named after it, in Terraform's local backend's own layout.
func TestWorkspaceStateFile(t *testing.T) {
	t.Parallel()
	for env, want := range map[string]string{
		"prod": "root/terraform.tfstate.d/prod/terraform.tfstate",
		"dev":  "root/terraform.tfstate.d/dev/terraform.tfstate",
	} {
		if got := filepath.ToSlash(WorkspaceStateFile("root", env)); got != want {
			t.Errorf("%s: got %q, want %q", env, got, want)
		}
	}
}

// Var files are given to a plan by their absolute paths, in their order.
func TestVarFileOptions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	want := []string{"vars/common.tfvars", "vars/prod/terraform.tfvars"}
	opts, paths, err := VarFileOptions(dir, want)
	if err != nil {
		t.Fatal(err)
	}
	var rel []string
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			t.Errorf("%s isn't absolute", path)
		}
		r, err := filepath.Rel(dir, path)
		if err != nil {
			t.Fatal(err)
		}
		rel = append(rel, filepath.ToSlash(r))
	}
	if strings.Join(rel, " ") != strings.Join(want, " ") || len(opts) != len(want) {
		t.Errorf("%v, %d options, want %v", rel, len(opts), want)
	}
}
