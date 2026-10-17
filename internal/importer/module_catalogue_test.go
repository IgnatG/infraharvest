// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IgnatG/infraharvest/internal/config"
)

func TestInstalledModule(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".terraform", "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"Modules":[{"Key":"","Source":"","Dir":"."},{"Key":"m","Source":"git::https://example.com/m.git//bucket?ref=v1","Dir":".terraform/modules/m/bucket"}]}`
	if err := os.WriteFile(filepath.Join(dir, ".terraform", "modules", "modules.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := installedModule(dir, "m")
	if want := filepath.Join(dir, ".terraform", "modules", "m", "bucket"); err != nil || got != want {
		t.Errorf("got %s, %v; want %s", got, err, want)
	}
	if _, err := installedModule(dir, "other"); err == nil {
		t.Error("want an error for a module that isn't there")
	}
}

// A directory's module needs no fetching; one that can't be matched is
// left out, as is one of another provider.
func TestCatalogueAdapters(t *testing.T) {
	bucket := t.TempDir()
	writeTF := func(dir, content string) {
		if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTF(bucket, "variable \"name\" {\n  type = string\n}\n\nresource \"aws_s3_bucket\" \"this\" {\n  bucket = var.name\n}\n")
	counted := t.TempDir()
	writeTF(counted, "resource \"aws_s3_bucket\" \"this\" {\n  count  = 2\n  bucket = \"b\"\n}\n")
	google := t.TempDir()
	writeTF(google, "resource \"google_storage_bucket\" \"this\" {\n  name     = \"b\"\n  location = \"EU\"\n}\n")

	list := catalogueAdapters(fetchAdapters(t.Context(), "", []config.Module{{Source: bucket}, {Source: counted}, {Source: google}}), "aws")
	if len(list) != 1 || list[0].Source != bucket || list[0].Anchor != "aws_s3_bucket" {
		t.Errorf("adapters: %+v", list)
	}
}

// The organisation's modules are fetched once per command, for every
// Import call and account, and kept in their order.
func TestOwnAdaptersFetchOnce(t *testing.T) {
	var modules []config.Module
	for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := "resource \"aws_s3_bucket\" \"this\" {\n  bucket = \"" + name + "\"\n}\n"
		if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		modules = append(modules, config.Module{Source: dir})
	}
	run := NewRun()
	first := run.shared.ownAdapters(t.Context(), "", modules)
	if len(first) != len(modules) {
		t.Fatalf("adapters: %+v", first)
	}
	for i, a := range first {
		if a.Source != modules[i].Source {
			t.Errorf("adapter %d is of %s, want %s", i, a.Source, modules[i].Source)
		}
	}
	// Gone now: fetching again would leave them out.
	for _, m := range modules {
		if err := os.RemoveAll(m.Source); err != nil {
			t.Fatal(err)
		}
	}
	if again := run.child().shared.ownAdapters(t.Context(), "", modules); len(again) != len(modules) {
		t.Errorf("fetched again: %+v", again)
	}
}
