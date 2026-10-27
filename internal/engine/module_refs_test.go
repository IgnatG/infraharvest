// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Release removes the modules a root took up that the import wrote and
// no other root calls, such as when the root is put back.
func TestModuleRefsRelease(t *testing.T) {
	t.Parallel()
	modules := t.TempDir()
	root, other := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	write := func(at string) error { return os.WriteFile(filepath.Join(at, "main.tf"), nil, 0o644) }
	var refs ModuleRefs
	for _, hold := range []struct{ root, module string }{{root, "only"}, {root, "shared"}, {other, "shared"}} {
		if _, err := refs.hold(hold.root, filepath.Join(modules, hold.module), write); err != nil {
			t.Fatal(err)
		}
	}
	if err := refs.Release(root); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(modules, "only")) || !exists(filepath.Join(modules, "shared")) {
		t.Errorf("want only the module no other root calls removed")
	}
	if err := refs.Release(other); err != nil || exists(filepath.Join(modules, "shared")) {
		t.Errorf("want the shared module removed once its last root lets go: %v", err)
	}
	if len(refs.users) != 0 || len(refs.written) != 0 || len(refs.byRoot) != 0 || len(refs.busy) != 0 {
		t.Errorf("released refs still count %v, %v, %v, %v", refs.users, refs.written, refs.byRoot, refs.busy)
	}
}

// Two imports count their own roots: one's release neither sees the
// roots of the other, nor removes the modules the other wrote, which an
// import that finds them calls as it would a module of an earlier run.
func TestModuleRefsOfTwoImports(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bucket_1a2b3c4d")
	write := func(at string) error { return os.WriteFile(filepath.Join(at, "main.tf"), nil, 0o644) }
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	var first, second ModuleRefs
	if wrote, err := first.hold(a, path, write); err != nil || !wrote {
		t.Fatalf("first: wrote %v, %v", wrote, err)
	}
	if wrote, err := second.hold(b, path, write); err != nil || wrote {
		t.Fatalf("second: wrote %v, %v; want the module found", wrote, err)
	}
	if err := second.Release(a); err != nil || !exists(path) {
		t.Fatalf("the second import released a root of the first: %v", err)
	}
	if err := second.Release(b); err != nil || !exists(path) {
		t.Fatalf("the second import removed a module it didn't write: %v", err)
	}
	if len(second.users) != 0 || len(second.byRoot) != 0 {
		t.Errorf("the second import still counts %v, %v", second.users, second.byRoot)
	}
	if first.users[path] != 1 || len(first.byRoot[a]) != 1 {
		t.Errorf("the first import counts %v, %v, want a alone", first.users, first.byRoot)
	}
}

// A module is written outside the refs' lock: a root that takes up
// another module meanwhile doesn't wait for the write.
func TestModuleRefsWriteOutsideTheLock(t *testing.T) {
	t.Parallel()
	modules := t.TempDir()
	var refs ModuleRefs
	writing, finish := make(chan struct{}), make(chan struct{})
	slow := func(at string) error {
		close(writing)
		<-finish
		return os.WriteFile(filepath.Join(at, "main.tf"), nil, 0o644)
	}
	held := make(chan error, 1)
	go func() {
		_, err := refs.hold("a", filepath.Join(modules, "slow"), slow)
		held <- err
	}()
	<-writing
	done := make(chan error, 1)
	go func() {
		_, err := refs.hold("b", filepath.Join(modules, "fast"), func(string) error { return nil })
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(10 * time.Second):
		t.Error("holding another module waited for a write")
	}
	close(finish)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
}

// Roots that take up the same modules at once each find them whole, and
// each is written once; roots that release them at once leave them to
// the roots still calling them.
func TestModuleRefsConcurrently(t *testing.T) {
	t.Parallel()
	modules := t.TempDir()
	var refs ModuleRefs
	var writes atomic.Int32
	write := func(at string) error {
		writes.Add(1)
		return os.WriteFile(filepath.Join(at, "main.tf"), []byte("# module\n"), 0o644)
	}
	const roots = 16
	paths := []string{filepath.Join(modules, "one"), filepath.Join(modules, "two")}
	// at runs f for each root in roots at once.
	at := func(roots []int, f func(root string) error) {
		var wg sync.WaitGroup
		errs := make(chan error, len(roots))
		for _, i := range roots {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- f(fmt.Sprintf("root%d", i))
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Error(err)
			}
		}
	}
	var all, even, odd []int
	for i := range roots {
		all = append(all, i)
		if i%2 == 0 {
			even = append(even, i)
		} else {
			odd = append(odd, i)
		}
	}
	at(all, func(root string) error {
		for _, path := range paths {
			if _, err := refs.hold(root, path, write); err != nil {
				return err
			}
			if !exists(filepath.Join(path, "main.tf")) {
				return fmt.Errorf("%s: %s held but not whole", root, path)
			}
		}
		return nil
	})
	if got := writes.Load(); got != int32(len(paths)) {
		t.Errorf("modules written %d times, want once each", got)
	}
	at(even, refs.Release)
	for _, path := range paths {
		if !exists(path) {
			t.Errorf("%s removed while roots still call it", path)
		}
	}
	at(odd, refs.Release)
	for _, path := range paths {
		if exists(path) {
			t.Errorf("%s outlived its last user", path)
		}
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
