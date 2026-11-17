// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package workers runs a lister's per-resource reads in goroutines that
// can't take the process down: a panic in one is caught where it
// happened and raised again on the goroutine that waits, so that the
// discoverer's own recovery (see discovery.Discover) turns it into that
// service's failure, and the other services carry on.
package workers

import (
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/IgnatG/infraharvest/internal/logging"
)

// Panic is the value Group.Wait panics with once a worker has panicked.
type Panic struct {
	// Value is what the worker panicked with.
	Value any
}

// Error reports the worker's panic. Its stack is in the log.
func (p *Panic) Error() string { return fmt.Sprintf("a worker panicked: %v", p.Value) }

// Unwrap returns the value the worker panicked with, if it is an error.
func (p *Panic) Unwrap() error {
	err, _ := p.Value.(error)
	return err
}

// Group runs functions in goroutines, at most its limit at once. The zero
// Group has no limit.
type Group struct {
	wg    sync.WaitGroup
	slots chan struct{}

	mu    sync.Mutex
	first *Panic
}

// New returns a Group that runs at most limit functions at once, or any
// number if limit is below one.
func New(limit int) *Group {
	g := &Group{}
	if limit > 0 {
		g.slots = make(chan struct{}, limit)
	}
	return g
}

// Go calls f in a goroutine of its own, waiting first for a free slot if
// the group is at its limit.
func (g *Group) Go(f func()) {
	if g.slots != nil {
		g.slots <- struct{}{}
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		if g.slots != nil {
			defer func() { <-g.slots }()
		}
		defer func() {
			if r := recover(); r != nil {
				logging.Errorf("worker panicked: %v\n%s", r, debug.Stack())
				g.mu.Lock()
				defer g.mu.Unlock()
				if g.first == nil {
					g.first = &Panic{Value: r}
				}
			}
		}()
		f()
	}()
}

// Wait returns once every function has returned. If any panicked, it
// panics with the first one's Panic, on the caller's goroutine.
func (g *Group) Wait() {
	g.wg.Wait()
	g.mu.Lock()
	p := g.first
	g.mu.Unlock()
	if p != nil {
		panic(p)
	}
}
