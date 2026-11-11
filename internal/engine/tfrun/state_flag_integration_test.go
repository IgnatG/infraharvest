// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package tfrun

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// TestLegacyStateFlag checks, with each engine, that the legacy -state
// flag the snapshot and statelessTerraform rely on still works for every
// command they pass it to: apply writes the state there and not in the
// root, plan reads it there, and state mv and state rm edit it there. The
// flag is deprecated in both engines; a release that drops it fails here
// rather than in an import (see the //nolint:staticcheck uses in
// snapshot.go and terraform.go). It uses terraform_data, which needs no
// provider download, and downloads Terraform if none is cached; OpenTofu
// must be installed, and must be found in CI:
//
//	go test -tags integration -run TestLegacyStateFlag ./internal/engine/tfrun/
func TestLegacyStateFlag(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cache := t.TempDir()
	engines := map[string]string{}
	execPath, err := TerraformBinary.Find(ctx, "", filepath.Join(cache, "terraform"))
	if err != nil {
		t.Fatal(err)
	}
	engines["terraform"] = execPath
	switch tofu, err := TofuBinary.Find(ctx, "", filepath.Join(cache, "tofu")); {
	case err == nil:
		engines["tofu"] = tofu
	case os.Getenv("CI") != "":
		t.Fatalf("CI is set, but OpenTofu isn't found: %v", err)
	default:
		t.Logf("not with OpenTofu: %v", err)
	}
	for name, execPath := range engines {
		t.Run(name, func(t *testing.T) {
			checkLegacyStateFlag(ctx, t, execPath, filepath.Join(cache, name+"-plugins"))
		})
	}
}

func checkLegacyStateFlag(ctx context.Context, t *testing.T, execPath, plugins string) {
	root := t.TempDir()
	config := "resource \"terraform_data\" \"r\" {\n  count = 3\n  input = \"x${count.index}\"\n}\n"
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	tf, err := New(root, execPath, plugins, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tf.Init(ctx); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), LocalStateFileName)
	//nolint:staticcheck // -state is the flag under test
	if err := tf.Apply(ctx, tfexec.State(state), tfexec.StateOut(state), tfexec.Lock(false), tfexec.Backup("-")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, LocalStateFileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("apply wrote the root's own state: %v", err)
	}
	addresses := func() []string {
		t.Helper()
		s, err := tf.ShowStateFile(ctx, state)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		if s.Values != nil {
			for _, r := range s.Values.RootModule.Resources {
				got = append(got, r.Address)
			}
		}
		slices.Sort(got)
		return got
	}
	if got, want := addresses(), []string{"terraform_data.r[0]", "terraform_data.r[1]", "terraform_data.r[2]"}; !slices.Equal(got, want) {
		t.Fatalf("applied: %v, want %v", got, want)
	}

	// A plan reads the state there: it has everything, so no changes.
	//nolint:staticcheck // -state is the flag under test
	changes, err := tf.Plan(ctx, tfexec.State(state), tfexec.Refresh(false), tfexec.Lock(false))
	if err != nil {
		t.Fatal(err)
	}
	if changes {
		t.Error("the plan with -state has changes: it didn't read the state there")
	}

	//nolint:staticcheck // -state is the flag under test
	if err := tf.StateMv(ctx, "terraform_data.r[0]", "terraform_data.moved", tfexec.State(state), tfexec.Lock(false)); err != nil {
		t.Fatal(err)
	}
	//nolint:staticcheck // -state is the flag under test
	if err := tf.StateRm(ctx, "terraform_data.r[1]", tfexec.State(state), tfexec.Lock(false)); err != nil {
		t.Fatal(err)
	}
	if got, want := addresses(), []string{"terraform_data.moved", "terraform_data.r[2]"}; !slices.Equal(got, want) {
		t.Errorf("after state mv and rm: %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(root, LocalStateFileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("state mv or rm wrote the root's own state: %v", err)
	}
}
