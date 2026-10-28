// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfrun

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tfjson "github.com/hashicorp/terraform-json"
)

// Roots share schemas only when the engine and their lock files are the
// same: another provider version, or another provider, is another key.
func TestSchemaKey(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// Roots that miss the same key together, as with --parallel, share one
// read of the schemas, and one failure, which isn't kept.
func TestSchemaStoreReadsOnceForConcurrentCallers(t *testing.T) {
	t.Parallel()
	const callers = 8
	want := &tfjson.ProviderSchemas{FormatVersion: "1.0"}
	for _, fail := range []bool{false, true} {
		s := &schemaStore{}
		var reads atomic.Int32
		release := make(chan struct{})
		read := func() (*tfjson.ProviderSchemas, error) {
			reads.Add(1)
			<-release
			if fail {
				return nil, errors.New("provider crashed")
			}
			return want, nil
		}
		var wg sync.WaitGroup
		for range callers {
			wg.Go(func() {
				got, err := s.get("k", read)
				if fail != (err != nil) || (!fail && got != want) {
					t.Errorf("fail %v: got %v, %v", fail, got, err)
				}
			})
		}
		// Every caller gets to the store while the read is in flight.
		time.Sleep(100 * time.Millisecond)
		close(release)
		wg.Wait()
		if n := reads.Load(); n != 1 {
			t.Errorf("fail %v: read %d times, want once", fail, n)
		}
		if _, ok := s.cached("k"); ok == fail {
			t.Errorf("fail %v: kept %v", fail, ok)
		}
	}
}
