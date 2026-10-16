// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// moduleRefs counts, across the roots one process generates, the roots
// that call each generated module directory: with --parallel, roots of
// different accounts share the modules directory, and two clusters of one
// shape share a module. A root's undo then removes a module only once no
// other root calls it, and only one this process wrote.
var moduleRefs = struct {
	sync.Mutex
	users   map[string]int      // module directory -> roots that call it
	written map[string]bool     // module directories this process wrote
	byRoot  map[string][]string // root directory -> the modules it holds
}{users: map[string]int{}, written: map[string]bool{}, byRoot: map[string][]string{}}

// holdModule makes root a user of the module at path, writing it with
// write first if nobody has: to a directory beside it, renamed into place,
// so no root sees it half written. It reports whether it wrote it.
func holdModule(root, path string, write func(string) error) (bool, error) {
	moduleRefs.Lock()
	defer moduleRefs.Unlock()
	wrote := false
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, err
		}
		tmp, err := os.MkdirTemp(filepath.Dir(path), "."+filepath.Base(path)+"-")
		if err != nil {
			return false, err
		}
		if err := write(tmp); err != nil {
			return false, errors.Join(err, os.RemoveAll(tmp))
		}
		if err := os.Chmod(tmp, 0o755); err != nil {
			return false, errors.Join(err, os.RemoveAll(tmp))
		}
		if err := os.Rename(tmp, path); err != nil {
			return false, errors.Join(err, os.RemoveAll(tmp))
		}
		moduleRefs.written[path] = true
		wrote = true
	} else if err != nil {
		return false, err
	}
	moduleRefs.users[path]++
	moduleRefs.byRoot[root] = append(moduleRefs.byRoot[root], path)
	return wrote, nil
}

// releaseModules ends root's use of the modules it holds, and removes each
// one this process wrote that no other root calls now.
func releaseModules(root string) error {
	moduleRefs.Lock()
	defer moduleRefs.Unlock()
	var errs []error
	for _, path := range moduleRefs.byRoot[root] {
		moduleRefs.users[path]--
		if moduleRefs.users[path] > 0 || !moduleRefs.written[path] {
			continue
		}
		delete(moduleRefs.users, path)
		delete(moduleRefs.written, path)
		errs = append(errs, os.RemoveAll(path))
	}
	delete(moduleRefs.byRoot, root)
	return errors.Join(errs...)
}
