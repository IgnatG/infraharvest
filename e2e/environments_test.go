// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
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

// resourceAddresses returns the addresses of the resource blocks of the
// root in dir, such as aws_sqs_queue.orders.
func resourceAddresses(t *testing.T, dir string) map[string]bool {
	t.Helper()
	addresses := map[string]bool{}
	for _, b := range configBlocks(t, dir) {
		if b.Type == "resource" {
			addresses[strings.Join(b.Labels, ".")] = true
		}
	}
	return addresses
}

// selectWorkspace selects workspace in the root in dir, initialised.
func selectWorkspace(ctx context.Context, t *testing.T, dir, execPath, plugins, workspace string) {
	t.Helper()
	tf, err := tfrun.New(dir, execPath, plugins, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tf.Init(ctx); err != nil {
		t.Fatalf("%s: terraform init: %v", dir, err)
	}
	if err := tf.WorkspaceSelect(ctx, workspace); err != nil {
		t.Fatalf("%s: terraform workspace select %s: %v", dir, workspace, err)
	}
}

// checkWorkspacePlan plans the merged root in dir as environment env, in
// its workspace, with its var files, and reports every resource the plan
// changes; it returns the addresses of those it imports, sorted. An
// import block of what the state already has imports nothing.
func checkWorkspacePlan(ctx context.Context, t *testing.T, dir, execPath, plugins, env string) []string {
	t.Helper()
	selectWorkspace(ctx, t, dir, execPath, plugins, env)
	plan := planRoot(ctx, t, dir, execPath, plugins, mergedVars(dir, env)...)
	var imports []string
	for _, rc := range plan.ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode {
			continue
		}
		if !rc.Change.Actions.NoOp() {
			changed := changedAttributes(rc.Change.Before, rc.Change.After)
			if rc.Change.Importing == nil || !rc.Change.Actions.Update() || !stateOnly(rc.Type, changed) {
				t.Errorf("%s, as %s: %s is planned to %v: %v", dir, env, rc.Address, rc.Change.Actions, changed)
				continue
			}
		}
		if rc.Change.Importing != nil {
			imports = append(imports, rc.Address)
		}
	}
	sort.Strings(imports)
	return imports
}

// applyMergedWorkspaces applies the merged root in dir as each of envs,
// into the workspace named after it, with its var files and the
// credentials credentials sets for it, as the root's README says for a
// root without a backend.
func applyMergedWorkspaces(ctx context.Context, t *testing.T, dir, execPath, plugins string, credentials func(env string), envs ...string) {
	t.Helper()
	for _, env := range envs {
		credentials(env)
		tf, err := tfrun.New(dir, execPath, plugins, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := tf.Init(ctx); err != nil {
			t.Fatalf("%s: terraform init: %v", env, err)
		}
		if err := tf.WorkspaceNew(ctx, env); err != nil {
			t.Fatalf("terraform workspace new %s: %v", env, err)
		}
		var vars []tfexec.ApplyOption
		for _, name := range []string{"common.tfvars", env + "/terraform.tfvars", env + "/imports.tfvars"} {
			vars = append(vars, tfexec.VarFile(filepath.Join(dir, "vars", filepath.FromSlash(name))))
		}
		if err := tf.Apply(ctx, vars...); err != nil {
			t.Fatalf("%s: terraform apply: %v", env, err)
		}
	}
}

// workspace returns Terraform in the merged root in dir, initialised, in
// env's workspace, which the environment has applied it into.
func workspace(ctx context.Context, t *testing.T, dir, execPath, plugins, env string) *tfrun.Runner {
	t.Helper()
	tf, err := tfrun.New(dir, execPath, plugins, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tf.Init(ctx); err != nil {
		t.Fatalf("%s: terraform init: %v", env, err)
	}
	if err := tf.WorkspaceSelect(ctx, env); err != nil {
		t.Fatalf("terraform workspace select %s: %v", env, err)
	}
	return tf
}

// mergedVarFiles are the var files environment env applies the merged
// root in dir with, as its README says.
func mergedVarFiles(dir, env string) []string {
	var files []string
	for _, name := range []string{"common.tfvars", env + "/terraform.tfvars", env + "/imports.tfvars"} {
		files = append(files, filepath.Join(dir, "vars", filepath.FromSlash(name)))
	}
	return files
}

// checkPlannedNothing checks that plan, of what, changes no resource.
func checkPlannedNothing(t *testing.T, what string, plan *tfjson.Plan) {
	t.Helper()
	for _, rc := range plan.ResourceChanges {
		if rc.Mode == tfjson.ManagedResourceMode && !rc.Change.Actions.NoOp() {
			t.Errorf("%s: %s is planned to %v, applied", what, rc.Address, rc.Change.Actions)
		}
	}
}

// applyWorkspace applies the merged root in dir as environment env, in
// its workspace, with its var files.
func applyWorkspace(ctx context.Context, t *testing.T, dir, execPath, plugins, env string) {
	t.Helper()
	tf := workspace(ctx, t, dir, execPath, plugins, env)
	apply := []tfexec.ApplyOption{tfexec.Lock(false)}
	for _, f := range mergedVarFiles(dir, env) {
		apply = append(apply, tfexec.VarFile(f))
	}
	if err := tf.Apply(ctx, apply...); err != nil {
		t.Fatalf("%s: terraform apply in its workspace: %v", env, err)
	}
}

// planWorkspace plans the merged root in dir as environment env, in its
// workspace, with its var files, and returns the plan.
func planWorkspace(ctx context.Context, t *testing.T, dir, execPath, plugins, env string) *tfjson.Plan {
	t.Helper()
	selectWorkspace(ctx, t, dir, execPath, plugins, env)
	return planRoot(ctx, t, dir, execPath, plugins, mergedVars(dir, env)...)
}
