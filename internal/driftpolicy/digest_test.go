// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package driftpolicy_test

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
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

// A file the digest can't read is an error, never a digest of the files
// read before it: a file gone mid-walk, as one a link leads nowhere
// from, or one that can't be read. So is a link out of the output
// directory, a link to a directory, which the walk doesn't follow, and a
// terraform.d directory. A link to a file under it is read where it
// leads.
func TestRootsDigestFailsClosed(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		setUp func(t *testing.T, out string) error
		// want is in the error, "" for a digest.
		want string
	}{
		"a link that leads nowhere": {func(_ *testing.T, out string) error {
			return os.Symlink(filepath.Join(out, "gone.tf"), filepath.Join(out, "aws", "x.tf"))
		}, "aws/x.tf can't be read"},
		"a link out of the output directory": {func(t *testing.T, out string) error {
			elsewhere := t.TempDir()
			writeFiles(t, elsewhere, map[string]string{"x.tf": "# elsewhere\n"})
			return os.Symlink(filepath.Join(elsewhere, "x.tf"), filepath.Join(out, "aws", "x.tf"))
		}, "aws/x.tf is a symbolic link out of the output directory"},
		"a link to a directory": {func(_ *testing.T, out string) error {
			return os.Symlink(filepath.Join(out, "aws"), filepath.Join(out, "gcp"))
		}, "gcp is a symbolic link to a directory"},
		"a terraform.d directory": {func(t *testing.T, out string) error {
			writeFiles(t, out, map[string]string{"aws/terraform.d/plugins/x": "#!/bin/sh\n"})
			return nil
		}, "aws/terraform.d is a terraform.d directory"},
		"a link under the output directory": {func(t *testing.T, out string) error {
			writeFiles(t, out, map[string]string{"shared/x.tf": "# shared\n"})
			return os.Symlink(filepath.Join("..", "shared", "x.tf"), filepath.Join(out, "aws", "x.tf"))
		}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out := t.TempDir()
			writeFiles(t, out, map[string]string{"aws/providers.tf": "provider \"aws\" {}\n", "aws/z.tf": "# last\n"})
			if err := tc.setUp(t, out); err != nil {
				t.Skipf("no symbolic links here: %v", err)
			}

			d, err := driftpolicy.RootsDigest(out)

			switch {
			case tc.want == "" && (err != nil || d == ""):
				t.Errorf("got %q, %v, want a digest", d, err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("got %q, %v, want an error with %q", d, err, tc.want)
			}
		})
	}
}

// A file that can't be read is an error.
func TestRootsDigestOfAnUnreadableFile(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	writeFiles(t, out, map[string]string{"aws/providers.tf": "provider \"aws\" {}\n"})
	path := filepath.Join(out, "aws", "providers.tf")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("the file can be read all the same, as by root")
	}
	if d, err := driftpolicy.RootsDigest(out); err == nil {
		t.Errorf("got %q, want an error", d)
	}
}

// ReadRoots hands each file to read once, and the digest is of the bytes
// read had, though the file changes after.
func TestReadRootsDigestsWhatItHands(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	files := map[string]string{"aws/providers.tf": "provider \"aws\" {}\n", "aws/vars/prod/terraform.tfvars": "x = 1\n"}
	writeFiles(t, out, files)
	want, err := driftpolicy.RootsDigest(out)
	if err != nil {
		t.Fatal(err)
	}
	handed := map[string]string{}

	got, err := driftpolicy.ReadRoots(out, func(path string, content []byte) error {
		rel, err := filepath.Rel(out, path)
		if err != nil {
			return err
		}
		handed[filepath.ToSlash(rel)] = string(content)
		// Changed after it was read: the digest isn't of this.
		return os.WriteFile(path, []byte("data \"external\" \"x\" {}\n"), 0o644)
	})

	if err != nil || got != want {
		t.Errorf("got %q (%v), want the digest of what was handed, %q", got, err, want)
	}
	if !maps.Equal(handed, files) {
		t.Errorf("handed %q, want %q", handed, files)
	}
}
