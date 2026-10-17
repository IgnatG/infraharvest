// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
)

// Roots share schemas only when the engine and their lock files are the
// same: another provider version, or another provider, is another key.
func TestSchemaKey(t *testing.T) {
	write := func(lock string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, LockFileName), []byte(lock), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	aws6 := "provider \"registry.terraform.io/hashicorp/aws\" {\n  version = \"6.0.0\"\n}\n"
	aws7 := "provider \"registry.terraform.io/hashicorp/aws\" {\n  version = \"7.0.0\"\n}\n"
	a, _ := schemaKey("terraform", write(aws6))
	b, _ := schemaKey("terraform", write(aws6))
	if a == "" || a != b {
		t.Errorf("the same lock file: keys %q and %q, want the same", a, b)
	}
	for name, key := range map[string]func() (string, bool){
		"another version": func() (string, bool) { return schemaKey("terraform", write(aws7)) },
		"another engine":  func() (string, bool) { return schemaKey("tofu", write(aws6)) },
	} {
		if k, ok := key(); !ok || k == a {
			t.Errorf("%s: key %q (%v), want another", name, k, ok)
		}
	}
	if _, ok := schemaKey("terraform", t.TempDir()); ok {
		t.Error("a root without a lock file has a key")
	}
}

// The store reads each key's schemas once, and doesn't keep a failure.
func TestSchemaStore(t *testing.T) {
	s := &schemaStore{}
	reads := 0
	want := &tfjson.ProviderSchemas{FormatVersion: "1.0"}
	read := func() (*tfjson.ProviderSchemas, error) {
		reads++
		return want, nil
	}
	if _, err := s.get("k", func() (*tfjson.ProviderSchemas, error) { return nil, errors.New("provider crashed") }); err == nil {
		t.Fatal("want the read's error")
	}
	for range 3 {
		got, err := s.get("k", read)
		if err != nil || got != want {
			t.Fatalf("got %v, %v", got, err)
		}
	}
	if reads != 1 {
		t.Errorf("read %d times, want once", reads)
	}
	for i := range maxCachedSchemas + 1 {
		if _, err := s.get(string(rune('a'+i)), read); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.schemas) > maxCachedSchemas {
		t.Errorf("keeps %d sets of schemas, want at most %d", len(s.schemas), maxCachedSchemas)
	}
}
