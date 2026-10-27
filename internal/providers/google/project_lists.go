// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"errors"
	"os"
	"sync"
)

// projectLists are lists of one kind that the process has made, by
// project and where they went (an emulator, or Google), each made once:
// the imports of a project's regions and of global share a list, which
// they only read.
type projectLists[T any] struct {
	mu    sync.Mutex
	byKey map[string]*projectList[T]
}

// projectList is one project's list: its value, or its error, once it
// has been made. mu is held while it runs, so that an import that wants
// it meanwhile waits for it rather than listing too.
type projectList[T any] struct {
	mu    sync.Mutex
	done  bool
	value T
	err   error
}

// once returns what list returns for project, calling it only the first
// time the process asks. A list that fails is kept too, so that the
// imports of the project's other regions don't make the failing call
// again, each after its retries (see retryTransport): only one that its
// context ended is not, so the next import lists again.
func (l *projectLists[T]) once(project string, list func() (T, error)) (T, error) {
	key := os.Getenv(EndpointEnv) + "\x00" + project
	l.mu.Lock()
	if l.byKey == nil {
		l.byKey = map[string]*projectList[T]{}
	}
	p, ok := l.byKey[key]
	if !ok {
		p = &projectList[T]{}
		l.byKey[key] = p
	}
	l.mu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return p.value, p.err
	}
	value, err := list()
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		var zero T
		return zero, err
	}
	p.value, p.err, p.done = value, err, true
	return value, err
}

// forget drops every list kept, for tests.
func (l *projectLists[T]) forget() {
	l.mu.Lock()
	l.byKey = nil
	l.mu.Unlock()
}
