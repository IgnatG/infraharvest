// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/IgnatG/infraharvest/internal/report"
)

// writeConfig writes content, a configuration file, into a new directory,
// and returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "infraharvest.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// mergeOf returns the merge of coverage whose merged root is path,
// relative to the output directory, failing the test if there is none.
func mergeOf(t *testing.T, coverage report.Report, path string) report.Merge {
	t.Helper()
	for _, m := range coverage.Merges {
		if m.Path == path {
			return m
		}
	}
	t.Fatalf("no merge into %s: %+v", path, coverage.Merges)
	return report.Merge{}
}

// checkMerged checks the merge of the stack whose merged root is path
// merged envs, with every check of each environment's gate passed, and
// that the roots it merged are gone from the output, kept under the
// checkpoints instead; it returns the merge.
func checkMerged(t *testing.T, out string, coverage report.Report, path string, envs ...string) report.Merge {
	t.Helper()
	m := mergeOf(t, coverage, path)
	if !m.Merged || m.Reason != "" || strings.Join(m.Environments, ",") != strings.Join(envs, ",") {
		t.Fatalf("%s: merged %v (%s), environments %v, want %v", path, m.Merged, m.Reason, m.Environments, envs)
	}
	for _, c := range m.Checks {
		if !c.Passed {
			t.Errorf("%s: %s failed: %v", path, c.Name, c.Details)
		}
	}
	for _, root := range m.Roots {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(root.Path))); err == nil {
			t.Errorf("%s (%s) is still in the output beside %s", root.Path, root.Env, path)
		}
		if _, err := os.Stat(filepath.Join(out, ".infraharvest", "premerge", filepath.FromSlash(root.Path), "versions.tf")); err != nil {
			t.Errorf("%s (%s) isn't kept under the checkpoints: %v", root.Path, root.Env, err)
		}
	}
	for _, d := range coverage.Directories {
		for _, root := range m.Roots {
			if d.Path == root.Path && d.MergedInto != path {
				t.Errorf("coverage.json: %s merged into %q, want %s", d.Path, d.MergedInto, path)
			}
		}
	}
	return m
}

// mergedVars are the var files environment env plans the merged root in
// dir with, as its README says.
func mergedVars(dir, env string) []tfexec.PlanOption {
	abs, _ := filepath.Abs(dir)
	return []tfexec.PlanOption{
		tfexec.VarFile(filepath.Join(abs, "vars", "common.tfvars")),
		tfexec.VarFile(filepath.Join(abs, "vars", env, "terraform.tfvars")),
		tfexec.VarFile(filepath.Join(abs, "vars", env, "imports.tfvars")),
	}
}

// checkMergedPlans plans the merged root in dir as each of envs, with
// its var files, and checks every resource is an import with no changes;
// it returns, by environment, how many resources of each type each
// imports. credentials, if set, sets each environment's credentials
// before its plan.
func checkMergedPlans(ctx context.Context, t *testing.T, dir, execPath, plugins string, credentials func(env string), envs ...string) map[string]map[string]int {
	t.Helper()
	imported := map[string]map[string]int{}
	for _, env := range envs {
		if credentials != nil {
			credentials(env)
		}
		imported[env] = checkNoChanges(ctx, t, dir, execPath, plugins, mergedVars(dir, env))
	}
	return imported
}
