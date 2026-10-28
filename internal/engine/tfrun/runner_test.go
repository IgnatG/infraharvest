// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestMergeEnv(t *testing.T) {
	t.Parallel()
	base := map[string]string{"Path": `C:\bin`, "TF_DATA_DIR": "/shared", "tf_log": "trace", "TF_VAR_name": "x", "HOME": "/home/me"}
	over := map[string]string{"PATH": `C:\tools`, "AWS_REGION": "eu-west-1"}

	got := mergeEnv(base, over, true)
	want := map[string]string{"PATH": `C:\tools`, "TF_LOG": "trace", "TF_VAR_name": "x", "HOME": "/home/me", "AWS_REGION": "eu-west-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("case-insensitive: got %v, want %v", got, want)
	}

	got = mergeEnv(base, over, false)
	want = map[string]string{"Path": `C:\bin`, "PATH": `C:\tools`, "tf_log": "trace", "TF_VAR_name": "x", "HOME": "/home/me", "AWS_REGION": "eu-west-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("case-sensitive: got %v, want %v", got, want)
	}
	if _, ok := base["TF_DATA_DIR"]; !ok {
		t.Error("base changed")
	}

	// A data directory of the runner's own wins; the process's never
	// reaches Terraform.
	for _, foldCase := range []bool{true, false} {
		if got := mergeEnv(base, map[string]string{"TF_DATA_DIR": "/private"}, foldCase); got["TF_DATA_DIR"] != "/private" {
			t.Errorf("foldCase %v: TF_DATA_DIR %q, want the runner's own", foldCase, got["TF_DATA_DIR"])
		}
	}
}

// The data directory a runner is given reaches the Terraform it runs, in
// place of the process's.
func TestNewTerraformDataDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake Terraform is a shell script")
	}
	t.Setenv("TF_DATA_DIR", "/shared")
	dir := t.TempDir()
	dump := filepath.Join(dir, "env")
	fake := filepath.Join(dir, "terraform")
	script := "#!/bin/sh\nenv > " + dump + "\nif [ \"$1\" = version ]; then echo '{\"terraform_version\":\"1.9.0\"}'; fi\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tf, err := New(filepath.Join(dir, "root"), fake, filepath.Join(dir, "plugins"), map[string]string{"TF_DATA_DIR": "/private"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := tf.Init(t.Context()); err != nil {
		t.Fatal(err)
	}

	env, err := os.ReadFile(dump)
	if err != nil || !slices.Contains(strings.Split(string(env), "\n"), "TF_DATA_DIR=/private") {
		t.Errorf("Terraform ran with:\n%s\n(%v), want TF_DATA_DIR=/private", env, err)
	}
}

// A runner with a fresh environment asks for it before each plan, and a
// plan fails, before Terraform runs, when it can't be had, as when the
// credentials can't be refreshed.
func TestFreshEnvTerraformAsksBeforeEachPlan(t *testing.T) {
	base := t.TempDir()
	refused := errors.New("the SSO session has expired")
	asked := 0
	tf, err := NewFreshEnv(filepath.Join(base, "root"), os.Args[0], filepath.Join(base, "plugins"), map[string]string{"AWS_ACCESS_KEY_ID": "AKIDFIRST"}, nil,
		func(context.Context) (map[string]string, error) {
			asked++
			return nil, refused
		})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := tf.PlanJSON(t.Context(), io.Discard); !errors.Is(err, refused) {
			t.Errorf("want the plan failed with the environment's error, got %v", err)
		}
	}
	if asked != 2 {
		t.Errorf("asked %d times for the environment, want once a plan", asked)
	}
}

// Terraform's environment has the process's, with what the provider adds
// on top, and the plugin cache.
func TestTerraformEnviron(t *testing.T) {
	t.Setenv("INFRAHARVEST_TEST_INHERITED", "kept")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDOLD")
	env := terraformEnviron("plugins", map[string]string{"AWS_ACCESS_KEY_ID": "AKIDNEW"})
	if env["INFRAHARVEST_TEST_INHERITED"] != "kept" || env["AWS_ACCESS_KEY_ID"] != "AKIDNEW" || env["TF_PLUGIN_CACHE_DIR"] != "plugins" {
		t.Errorf("environment: inherited %q, key %q, cache %q", env["INFRAHARVEST_TEST_INHERITED"], env["AWS_ACCESS_KEY_ID"], env["TF_PLUGIN_CACHE_DIR"])
	}
}

// Output directories such as generated/aws/sqs don't exist before the
// first import into them.
func TestNewTerraformCreatesDirs(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "generated", "aws", "sqs")
	cache := filepath.Join(base, "cache", "plugins")

	if _, err := New(dir, os.Args[0], cache, nil, nil); err != nil {
		t.Fatal(err)
	}

	for _, d := range []string{dir, cache} {
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			t.Errorf("%s not created: %v", d, err)
		}
	}
}
