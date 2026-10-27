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

// ModuleRefs counts, across the roots one import generates, the roots
// that call each generated module directory: with --parallel, roots of
// different accounts share the modules directory, and two clusters of one
// shape share a module. A root's undo then removes a module only once no
// other root calls it, and only one the import wrote (see Release). Roots
// that share a modules directory share one ModuleRefs (see
// Options.Modules); it belongs to the import, so what it counts goes with
// it. The zero value counts nothing yet, and is ready to use.
type ModuleRefs struct {
	mu      sync.Mutex
	users   map[string]int      // module directory -> roots that call it
	written map[string]bool     // module directories the import wrote
	byRoot  map[string][]string // root directory -> the modules it holds
	// busy are the module directories being written or removed, each
	// with a channel closed once that is done. The disk work happens
	// without mu, so that roots whose modules differ don't wait on it,
	// and a root that wants a busy module waits for it alone.
	busy map[string]chan struct{}
}

// hold makes root a user of the module at path, writing it with write
// first if nobody has: to a directory beside it, renamed into place, so
// no root sees it half written. It reports whether it wrote it.
func (m *ModuleRefs) hold(root, path string, write func(string) error) (bool, error) {
	m.mu.Lock()
	for {
		done, ok := m.busy[path]
		if !ok {
			break
		}
		m.mu.Unlock()
		<-done
		m.mu.Lock()
	}
	if _, err := os.Stat(path); err == nil {
		m.add(root, path)
		m.mu.Unlock()
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		m.mu.Unlock()
		return false, err
	}
	done := m.claim(path)
	m.mu.Unlock()

	err := writeModuleDir(path, write)

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.busy, path)
	close(done)
	if err != nil {
		return false, err
	}
	if m.written == nil {
		m.written = map[string]bool{}
	}
	m.written[path] = true
	m.add(root, path)
	return true, nil
}

// add counts root as a user of the module at path. m.mu is held.
func (m *ModuleRefs) add(root, path string) {
	if m.users == nil {
		m.users, m.byRoot = map[string]int{}, map[string][]string{}
	}
	m.users[path]++
	m.byRoot[root] = append(m.byRoot[root], path)
}

// claim marks the module at path busy, and returns the channel to close
// once it isn't. m.mu is held.
func (m *ModuleRefs) claim(path string) chan struct{} {
	if m.busy == nil {
		m.busy = map[string]chan struct{}{}
	}
	done := make(chan struct{})
	m.busy[path] = done
	return done
}

// writeModuleDir writes the module at path with write, into a directory
// beside it that it then renames into place.
func writeModuleDir(path string, write func(string) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(path), "."+filepath.Base(path)+"-")
	if err != nil {
		return err
	}
	if err := write(tmp); err != nil {
		return errors.Join(err, os.RemoveAll(tmp))
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return errors.Join(err, os.RemoveAll(tmp))
	}
	if err := os.Rename(tmp, path); err != nil {
		return errors.Join(err, os.RemoveAll(tmp))
	}
	return nil
}

// Release ends the use the root in dir makes of the generated modules it
// holds, such as when what Generate wrote there is put back as it was,
// and removes each one the import wrote that no other root calls now.
func (m *ModuleRefs) Release(dir string) error {
	m.mu.Lock()
	var gone []string
	for _, path := range m.byRoot[dir] {
		m.users[path]--
		if m.users[path] > 0 {
			continue
		}
		delete(m.users, path)
		if m.written[path] {
			delete(m.written, path)
			gone = append(gone, path)
		}
	}
	delete(m.byRoot, dir)
	done := make([]chan struct{}, len(gone))
	for i, path := range gone {
		done[i] = m.claim(path)
	}
	m.mu.Unlock()

	var errs []error
	for _, path := range gone {
		errs = append(errs, os.RemoveAll(path))
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for i, path := range gone {
		delete(m.busy, path)
		close(done[i])
	}
	return errors.Join(errs...)
}
