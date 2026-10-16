// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

// An edit that fails after changing a file leaves nothing of it behind.
func TestVerifyRestoresWhenTheEditFails(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, GeneratedFileName, networkGenerated)
	tf := &fakeTerraform{dir: dir}
	failure := errors.New("halfway")
	edit := func() (bool, error) {
		if err := os.WriteFile(filepath.Join(dir, GeneratedFileName), []byte("# changed\n"), 0o644); err != nil {
			return false, err
		}
		if err := os.WriteFile(filepath.Join(dir, LocalsFileName), []byte("locals {}\n"), 0o644); err != nil {
			return false, err
		}
		return true, failure
	}

	kept, err := verify(context.Background(), tf, dir, changeSummary{}, nil, nil, edit, GeneratedFileName, LocalsFileName)
	if kept || !errors.Is(err, failure) {
		t.Fatalf("got kept=%v err=%v, want the edit's error", kept, err)
	}

	if got := readFile(t, dir, GeneratedFileName); got != networkGenerated {
		t.Errorf("generated.tf not restored:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, LocalsFileName)); err == nil {
		t.Error("locals.tf not removed")
	}
	if len(tf.calls) != 0 {
		t.Errorf("planned after a failed edit: %v", tf.calls)
	}
}

// An edit is kept only if each resource plans the change it had, not only
// the same totals: one that hides another change in a resource that
// already updates, such as its labels after an import, is undone.
func TestVerifyComparesEachResourcesChange(t *testing.T) {
	baseline := changeSummary{Change: 1}
	updatesTags := resourceChange("aws_subnet.a", tfjson.Actions{tfjson.ActionUpdate}, map[string]any{"tags": map[string]any{}}, map[string]any{"tags": map[string]any{"a": "b"}})
	changes := map[string]string{"aws_subnet.a": changeSignature(updatesTags)}
	edit := func(dir string) func() (bool, error) {
		return func() (bool, error) {
			return true, os.WriteFile(filepath.Join(dir, GeneratedFileName), []byte("# edited\n"), 0o644)
		}
	}
	for name, tt := range map[string]struct {
		planned *tfjson.ResourceChange
		kept    bool
	}{
		"the same change": {updatesTags, true},
		"another change too": {resourceChange("aws_subnet.a", tfjson.Actions{tfjson.ActionUpdate},
			map[string]any{"tags": map[string]any{}, "cidr_block": "10.0.1.0/24"},
			map[string]any{"tags": map[string]any{"a": "b"}, "cidr_block": "10.0.9.0/24"}), false},
	} {
		dir := t.TempDir()
		writeConfig(t, dir, GeneratedFileName, networkGenerated)
		tf := &fakeTerraform{dir: dir, plans: []fakePlan{{summary: baseline}}, showns: []*tfjson.Plan{{ResourceChanges: []*tfjson.ResourceChange{tt.planned}}}}

		kept, err := verify(context.Background(), tf, dir, baseline, changes, nil, edit(dir), GeneratedFileName)
		if err != nil || kept != tt.kept {
			t.Errorf("%s: got kept=%v err=%v, want kept=%v", name, kept, err, tt.kept)
		}
		if restored := readFile(t, dir, GeneratedFileName) == networkGenerated; restored == tt.kept {
			t.Errorf("%s: generated.tf restored=%v", name, restored)
		}
	}
}
