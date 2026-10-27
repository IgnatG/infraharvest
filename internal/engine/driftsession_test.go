// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/IgnatG/infraharvest/internal/engine/tfrun"
)

// A session of an environment of a root environments share pulls its
// workspace's state, stages its var files with the copy, plans with them,
// counts its import IDs, not the root's import blocks, as not applied,
// and edits only its var file; it removes the copy once closed.
func TestDriftSessionOfAnEnvironment(t *testing.T) {
	t.Parallel()
	dir, staging := acceptRoot(t, map[string]string{
		"main.tf":                                   envMain,
		"vars/dev/terraform.tfvars":                 devVars,
		"vars/prod/terraform.tfvars":                prodVars,
		"vars/dev/imports.tfvars":                   "import_ids = {\n  \"aws_sqs_queue.jobs\" = \"jobs\"\n}\n",
		"terraform.tfstate.d/dev/terraform.tfstate": driftState,
	})
	ctx := context.Background()
	s, err := openDriftSession(dir, staging, DriftOptions{Environment: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	applied, err := s.stage(ctx, &fakeDriftTerraform{name: "root", calls: &calls})
	if err != nil || !applied {
		t.Fatalf("stage: %v, %v", applied, err)
	}
	if len(calls) != 0 {
		t.Errorf("the workspace's state is read from the root, not pulled: %v", calls)
	}
	staged := filepath.Join(staging, "vars", "dev", "terraform.tfvars")
	if content, err := os.ReadFile(staged); err != nil || string(content) != devVars {
		t.Errorf("the staged var file: %q, %v", content, err)
	}
	copied := &fakeDriftTerraform{name: "staged", calls: &calls, shown: &tfjson.State{Values: &tfjson.StateValues{RootModule: &tfjson.StateModule{
		Resources: []*tfjson.StateResource{{Address: "aws_sqs_queue.orders", Mode: tfjson.ManagedResourceMode}},
	}}}}
	if err := s.open(ctx, func(string) (tfrun.DriftTerraform, error) { return copied, nil }); err != nil {
		t.Fatal(err)
	}
	inState, err := s.inState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.notApplied(inState); !reflect.DeepEqual(got, []string{"aws_sqs_queue.jobs"}) {
		t.Errorf("not applied %v", got)
	}
	if names, err := s.placeholders(); err != nil || len(names) != 0 {
		t.Errorf("placeholders %v, %v: the var files set every variable", names, err)
	}
	if _, err := s.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if !hasOption[tfexec.PlanOption](copied.planOpts[0], tfexec.VarFile(staged)) || !hasOption[tfexec.PlanOption](copied.planOpts[0], tfexec.RefreshOnly(true)) {
		t.Errorf("the refresh plan's options %v", copied.planOpts[0])
	}
	root, err := s.contents()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := root[envVarsPath("dev")]; !ok {
		t.Errorf("the files drift edits lack dev's var file: %v", sortedKeys(root))
	}
	e, err := s.editor(&tfjson.Plan{}, &tfjson.Plan{}, root)
	if err != nil {
		t.Fatal(err)
	}
	if e.env != "dev" || e.envVars != envVarsPath("dev") {
		t.Errorf("the editor edits %q in %q", e.env, e.envVars)
	}
	s.close()
	if _, err := os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy is left: %v", err)
	}
}

// A session of a root of its own without state stages nothing, so
// closing it leaves staging as it was.
func TestDriftSessionWithoutState(t *testing.T) {
	t.Parallel()
	dir, staging := driftRoot(t, map[string]string{})
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := openDriftSession(dir, staging, DriftOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	applied, err := s.stage(context.Background(), &fakeDriftTerraform{name: "root", calls: &calls})
	if err != nil || applied {
		t.Fatalf("stage: %v, %v", applied, err)
	}
	s.close()
	if _, err := os.Stat(staging); err != nil {
		t.Errorf("staging removed: %v", err)
	}
}
