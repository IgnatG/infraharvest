// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/gate"
	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// refactorBefore is the root as an earlier import left it, applied: two
// instances of a resource, a module call of two resources, and a
// random_string.
const refactorBefore = `resource "terraform_data" "group" {
  for_each = toset(["a", "b"])
  input    = each.key
}

module "m" {
  source = "./m"
}

resource "random_string" "s" {
  length = 12
}
`

// refactorAfter is the root generated again: one instance of the
// resource and one resource of the module call went to another root, and
// the random_string is a random_password now, with the same import ID.
// The moved blocks move what leaves to resources of the root alone, which
// the removed blocks forget; the random_string is forgotten, and imported
// again as the random_password.
const refactorAfter = `resource "terraform_data" "group" {
  for_each = toset(["a"])
  input    = each.key
}

module "m" {
  source = "./m"
}

resource "random_password" "s" {
  length = 12
}

import {
  to = random_password.s
  id = "%s"
}

moved {
  from = terraform_data.group["b"]
  to   = terraform_data.departed_group_b
}

moved {
  from = module.m.terraform_data.y
  to   = terraform_data.departed_m_y
}

removed {
  from = terraform_data.departed_group_b

  lifecycle {
    destroy = false
  }
}

removed {
  from = terraform_data.departed_m_y

  lifecycle {
    destroy = false
  }
}

removed {
  from = random_string.s

  lifecycle {
    destroy = false
  }
}
`

// TestRefactorBlocksWithRealEngines plans the moved and removed blocks
// --regenerate writes for what a moved block alone can't follow, over the
// state Terraform, and OpenTofu where it is installed, applied: an
// instance of a resource, and a resource of a module call that keeps
// another, moved to resources of the root alone and forgotten there (see
// Removal.Instance), and a resource of another type with the same import
// ID forgotten and imported again (see Retype). The plan forgets and
// imports, and destroys and creates nothing; applied, the root plans no
// change. It downloads the engine and the random provider, so it runs in
// CI.
func TestRefactorBlocksWithRealEngines(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engines := map[string]string{}
	execPath, err := tfrun.TerraformBinary.Find(ctx, "", filepath.Join(cache, "terraform"))
	if err != nil {
		t.Fatal(err)
	}
	engines["terraform"] = execPath
	// CI installs OpenTofu, so there it must be found.
	switch tofu, err := tfrun.TofuBinary.Find(ctx, "", filepath.Join(cache, "tofu")); {
	case err == nil:
		engines["tofu"] = tofu
	case os.Getenv("CI") != "":
		t.Fatalf("CI is set, but OpenTofu isn't found: %v", err)
	default:
		t.Logf("not with OpenTofu: %v", err)
	}
	for name, execPath := range engines {
		t.Run(name, func(t *testing.T) {
			checkRefactorBlocks(ctx, t, execPath, filepath.Join(cache, name+"-plugins"))
		})
	}
}

// checkRefactorBlocks applies refactorBefore with the engine at execPath,
// then plans and applies refactorAfter over its state, as
// TestRefactorBlocksWithRealEngines says.
func checkRefactorBlocks(ctx context.Context, t *testing.T, execPath, plugins string) {
	dir := t.TempDir()
	writeRefactorRoot(t, dir, refactorBefore, "x", "y")
	tf, err := tfrun.New(dir, execPath, plugins, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tf.Init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := tf.Apply(ctx); err != nil {
		t.Fatalf("apply: %v", err)
	}
	state, err := tf.Show(ctx)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	value := ""
	for _, r := range state.Values.RootModule.Resources {
		if r.Address == "random_string.s" {
			value, _ = r.AttributeValues["result"].(string)
		}
	}
	if value == "" {
		t.Fatalf("no random_string.s in the state: %+v", state.Values.RootModule.Resources)
	}

	// Generated again: the random_string's value is its import ID, as a
	// quoted string without templates.
	id := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "${", "$${", "%{", "%%{").Replace(value)
	writeRefactorRoot(t, dir, strings.Replace(refactorAfter, "%s", id, 1), "x")
	check, err := gate.MovesCheck(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Passed {
		t.Errorf("the moves check fails: %v", check.Details)
	}
	if err := tf.Init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	plan := planRefactor(ctx, t, tf)
	forgotten := map[string]string{}
	imported := false
	for _, rc := range plan.ResourceChanges {
		if rc.Mode != tfjson.ManagedResourceMode {
			continue
		}
		switch {
		case rc.Change.Actions.Create() || rc.Change.Actions.Delete() || rc.Change.Actions.Replace():
			t.Errorf("%s is planned to %v", rc.Address, rc.Change.Actions)
		case rc.Change.Actions.Forget():
			forgotten[rc.Address] = rc.PreviousAddress
		case rc.Address == "random_password.s" && rc.Change.Importing != nil:
			imported = true
		}
	}
	for address, from := range map[string]string{
		"terraform_data.departed_group_b": `terraform_data.group["b"]`,
		"terraform_data.departed_m_y":     "module.m.terraform_data.y",
		"random_string.s":                 "",
	} {
		// The plan may name where a moved resource was, or not.
		if got, ok := forgotten[address]; !ok || (got != "" && got != from) {
			t.Errorf("%s: forgotten %v, from %q, want from %q; forgotten %v", address, ok, got, from, forgotten)
		}
	}
	if !imported || len(forgotten) != 3 {
		t.Errorf("random_password.s imported: %v; forgotten %v", imported, forgotten)
	}

	// Applied, the state has what the root declares, and nothing changes.
	if err := tf.Apply(ctx); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, rc := range planRefactor(ctx, t, tf).ResourceChanges {
		if rc.Mode == tfjson.ManagedResourceMode && (!rc.Change.Actions.NoOp() || rc.Change.Importing != nil || rc.PreviousAddress != "") {
			t.Errorf("after the apply, %s is planned to %v", rc.Address, rc.Change.Actions)
		}
	}
}

// writeRefactorRoot writes main, the root's configuration, into dir, and
// its module m, with a terraform_data resource for each label.
func writeRefactorRoot(t *testing.T, dir, main string, labels ...string) {
	t.Helper()
	var module strings.Builder
	for _, label := range labels {
		module.WriteString("resource \"terraform_data\" \"" + label + "\" {\n  input = \"" + label + "\"\n}\n\n")
	}
	if err := os.MkdirAll(filepath.Join(dir, "m"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "m", "main.tf"), []byte(module.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
}

// planRefactor plans the root of tf and returns the plan.
func planRefactor(ctx context.Context, t *testing.T, tf *tfrun.Runner) *tfjson.Plan {
	t.Helper()
	planFile := filepath.Join(t.TempDir(), "root.tfplan")
	if _, err := tf.Plan(ctx, tfexec.Out(planFile)); err != nil {
		t.Fatalf("plan: %v", err)
	}
	plan, err := tf.ShowPlanFile(ctx, planFile)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	return plan
}
