// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/infraharvest/infraharvest-cli/internal/config"
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

	list := catalogueAdapters(fetchAdapters(t.Context(), "", "", "aws", []config.Module{{Source: bucket}, {Source: counted}, {Source: google}}), "aws")
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
	first := run.shared.ownAdapters(t.Context(), "", "", "aws", modules)
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
	if again := run.child().shared.ownAdapters(t.Context(), "", "", "aws", modules); len(again) != len(modules) {
		t.Errorf("fetched again: %+v", again)
	}
}

// An import leaves out, before fetching, the registry modules of the
// other providers it imports, and keeps those it can't tell.
func TestForOtherProvider(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		source, provider string
		other            bool
	}{
		{"example.com/platform/network/aws", "aws", false},
		{"example.com/platform/network/google", "aws", true},
		{"platform/network/azurerm", "aws", true},
		{"example.com/platform/network/google//modules/subnets", "google-beta", false},
		{"example.com/platform/network/azure", "aws", false}, // not a provider infraharvest imports
		{"git::https://example.com/modules.git//google?ref=v1.0.0", "aws", false},
		{"./modules/google", "aws", false},
	} {
		if got := forOtherProvider(config.Module{Source: c.source, Version: "1.0.0"}, c.provider); got != c.other {
			t.Errorf("%s for %s: got %v, want %v", c.source, c.provider, got, c.other)
		}
	}
}

// A module that can't change is found in the cache without fetching it;
// one that can, a git tag, is fetched again.
func TestFetchModuleCached(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	pinned := config.Module{Source: "example.com/platform/network/aws", Version: "1.2.0"}
	cached := cachedModule(cacheDir, pinned)
	moduleDir := filepath.Join(cached, ".terraform", "modules", "m")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"Modules":[{"Key":"m","Dir":".terraform/modules/m"}]}`
	if err := os.WriteFile(filepath.Join(cached, ".terraform", "modules", "modules.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	// No engine: fetching would fail.
	dir, cleanup, err := fetchModule(t.Context(), "", cacheDir, pinned)
	if err != nil || dir != moduleDir {
		t.Fatalf("got %s, %v; want %s", dir, err, moduleDir)
	}
	cleanup()
	if !isDir(moduleDir) {
		t.Error("cleanup removed the cached module")
	}
	tagged := config.Module{Source: "git::https://example.com/modules.git//network?ref=v1.2.0"}
	if _, _, err := fetchModule(t.Context(), "", cacheDir, tagged); err == nil {
		t.Error("a module at a tag came from the cache")
	}
}
