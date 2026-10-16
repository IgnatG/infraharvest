// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package selection

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Loading any file doesn't panic, nor does deciding with what it loads; a
// file that loads saves to one that loads, and saving that again gives the
// same bytes.
func FuzzLoad(f *testing.F) {
	f.Add(edited)
	f.Add(`version: 1
defaults: { include: false }
rules:
  - exclude: { tags: { env: dev, owner: "" } }
  - include: { type: "aws_*", id: ["a*", "*b"], name: "*" }
resources:
  - type: aws_iam_role
    id: app
    scope: aws/123456789012/global
    tags: { team: platform }
    include: true
    new: true
  - type: aws_iam_role
    id: app
    scope: aws/210987654321/global
    include: false
    reason: already managed by Terraform (aws/global/default.tfstate)
`)
	f.Add("version: 1\nrules:\n  - include: {}\n    exclude: {}\n")
	f.Add("version: 2\nresources: []\n")
	f.Add("version: 1\nunknown: field\n")
	f.Add("version: 1\nresources:\n  - { type: x, id: \"\\u00e9*\", tags: { k: ~ } }\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, content string) {
		dir := t.TempDir()
		path := filepath.Join(dir, "selection.yaml")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		file, err := Load(path)
		if err != nil {
			return
		}
		for _, r := range file.Resources {
			_ = file.DecideIn(r.Scope, r.Type, r.ID, r.Name, r.Tags)
		}
		_ = file.Decide("aws_vpc", "vpc-1", "main")
		_ = file.Has("aws_vpc", "vpc-1")

		saved := saveAndRead(t, file, filepath.Join(dir, "saved.yaml"))
		again, err := Load(filepath.Join(dir, "saved.yaml"))
		if err != nil {
			t.Fatalf("the saved file doesn't load: %v\n%s", err, saved)
		}
		if resaved := saveAndRead(t, again, filepath.Join(dir, "resaved.yaml")); !bytes.Equal(saved, resaved) {
			t.Errorf("saving the saved file changed it:\n%s\nto:\n%s", saved, resaved)
		}
	})
}

// saveAndRead saves f to path and returns what it wrote.
func saveAndRead(t *testing.T, f *File, path string) []byte {
	t.Helper()
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}
