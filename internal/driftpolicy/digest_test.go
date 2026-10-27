// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package driftpolicy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IgnatG/infraharvest/internal/driftpolicy"
)

// writeFiles writes files, by path relative to dir, under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The digest changes with any file Terraform reads under the output
// directory, and with nothing else: not the report, the state, the
// .terraform directories.
func TestRootsDigest(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	writeFiles(t, out, map[string]string{"aws/1/eu-west-2/providers.tf": "provider \"aws\" {}\n"})
	digest := func() string {
		t.Helper()
		d, err := driftpolicy.RootsDigest(out)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	before := digest()
	writeFiles(t, out, map[string]string{
		"report/drift.md":                              "# Drift\n",
		"aws/1/eu-west-2/terraform.tfstate":            "{}",
		"aws/1/eu-west-2/.terraform/modules/m/main.tf": "# installed\n",
	})
	if digest() != before {
		t.Errorf("the digest changed with files it leaves out")
	}
	for _, name := range []string{"aws/1/eu-west-2/s3.tf", "aws/1/eu-west-2/x.tf.json", "aws/1/eu-west-2/x.tofu", "aws/1/eu-west-2/vars/prod/terraform.tfvars", "aws/1/eu-west-2/.terraform.lock.hcl", "aws/1/eu-west-2/vars/prod/backend.hcl", "modules/queue_a1b2c3/main.tf"} {
		writeFiles(t, out, map[string]string{name: "# changed\n"})
		if after := digest(); after == before {
			t.Errorf("the digest didn't change with %s", name)
		} else {
			before = after
		}
	}
	if d, err := driftpolicy.RootsDigest(filepath.Join(out, "missing")); err != nil || d == "" {
		t.Errorf("no output directory: %q, %v", d, err)
	}
}
