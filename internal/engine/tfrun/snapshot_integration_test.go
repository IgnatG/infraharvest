// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package tfrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// countingApply counts the applies of a runner.
type countingApply struct {
	*Runner
	applies int
}

func (c *countingApply) Apply(ctx context.Context, opts ...tfexec.ApplyOption) error {
	c.applies++
	return c.Runner.Apply(ctx, opts...)
}

// TestReconcileMovesWithTerraform moves resources of a snapshot into a
// module with a real Terraform: in one apply of moved blocks where the
// module's configuration matches the snapshot, and one state mv at a time
// where it doesn't, which the apply's check refuses. It uses
// terraform_data, which needs no provider download, but downloads
// Terraform if none is cached:
//
//	go test -tags integration -run TestReconcileMovesWithTerraform ./internal/engine/tfrun/
func TestReconcileMovesWithTerraform(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cache := t.TempDir()
	execPath, err := TerraformBinary.Find(ctx, "", filepath.Join(cache, "terraform"))
	if err != nil {
		t.Fatal(err)
	}
	n := moveAllFrom + 2
	for _, tc := range []struct {
		name string
		// input is the module's input of resource i, which matches the
		// snapshot's "x<i>" or not.
		input   string
		applies int
	}{
		{name: "one apply", input: `"x${each.value}"`, applies: 1},
		{name: "one at a time", input: `"changed"`, applies: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(dir, name, content string) {
				t.Helper()
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// The snapshot: n resources, and one more that goes.
			write(root, "main.tf", fmt.Sprintf("resource \"terraform_data\" \"r\" {\n  count = %d\n  input = \"x${count.index}\"\n}\n", n+1))
			tf, err := New(root, execPath, filepath.Join(cache, "plugins"), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := tf.Init(ctx); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			s := &Snapshot{dir: dir, state: filepath.Join(dir, LocalStateFileName), ids: map[string]string{}}
			if err := tf.Apply(ctx, tfexec.State(s.state), tfexec.StateOut(s.state), tfexec.Lock(false), tfexec.Backup("-")); err != nil { //nolint:staticcheck // as the snapshot
				t.Fatal(err)
			}
			var blocks []ImportBlock
			var want []string
			for i := range n + 1 {
				id := fmt.Sprintf("id-%d", i)
				s.ids[fmt.Sprintf("terraform_data.r[%d]", i)] = id
				if i < n {
					to := fmt.Sprintf("module.m.terraform_data.r[\"%d\"]", i)
					blocks = append(blocks, ImportBlock{To: to, ID: id})
					want = append(want, to)
				}
			}
			blocks = append(blocks, ImportBlock{To: "terraform_data.kept", ID: "kept"})

			// The root as the next plan reads it: the resources are in a
			// module, and a new one is at the top level.
			if err := os.Remove(filepath.Join(root, "main.tf")); err != nil {
				t.Fatal(err)
			}
			main := "module \"m\" {\n  source = \"./m\"\n}\n\nresource \"terraform_data\" \"kept\" {\n  input = \"k\"\n}\n"
			module := fmt.Sprintf("resource \"terraform_data\" \"r\" {\n  for_each = toset([for i in range(%d) : tostring(i)])\n  input    = %s\n}\n", n, tc.input)
			write(root, "main.tf", main)
			write(filepath.Join(root, "m"), "main.tf", module)
			if err := tf.Init(ctx); err != nil {
				t.Fatal(err)
			}

			var copied *countingApply
			copies := func(context.Context) (*MoveCopy, error) {
				dir := t.TempDir()
				write(dir, "main.tf", main)
				write(filepath.Join(dir, "m"), "main.tf", module)
				r, err := New(dir, execPath, filepath.Join(cache, "plugins"), nil, nil)
				if err != nil {
					return nil, err
				}
				copied = &countingApply{Runner: r}
				return &MoveCopy{Dir: dir, Plan: r, Apply: copied, Resources: []string{"terraform_data.kept"}, Remove: func() {}}, nil
			}
			kept, err := s.reconcile(ctx, tf, blocks, time.Minute, copies)
			if err != nil {
				t.Fatal(err)
			}
			if copied == nil || copied.applies != tc.applies {
				t.Errorf("applies in the copy: %+v, want %d", copied, tc.applies)
			}
			if len(kept) != n {
				t.Errorf("kept %d, want %d", len(kept), n)
			}
			state, err := tf.ShowStateFile(ctx, s.state)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, r := range state.Values.RootModule.Resources {
				got = append(got, r.Address)
			}
			for _, m := range state.Values.RootModule.ChildModules {
				for _, r := range m.Resources {
					got = append(got, r.Address)
				}
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("the snapshot has:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}
