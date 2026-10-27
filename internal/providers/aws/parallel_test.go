// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// inParallel reads every index, never more than parallel at once.
func TestInParallel(t *testing.T) {
	const n, parallel = 20, 3
	var mu sync.Mutex
	running, most := 0, 0
	read := make([]bool, n)
	inParallel(n, parallel, func(i int) {
		mu.Lock()
		running++
		most = max(most, running)
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		read[i] = true
	})
	for i, ok := range read {
		if !ok {
			t.Errorf("index %d wasn't read", i)
		}
	}
	if most > parallel || most == 0 {
		t.Errorf("%d ran at once, want at most %d", most, parallel)
	}
}

// overlapWait is how long a call waits at an overlap for the others.
const overlapWait = 2 * time.Second

// overlap is a meeting point for the calls of a fake API: each waits there
// until n calls have arrived, so a lister that makes them one at a time
// never gets n there, and the test fails rather than hangs.
type overlap struct {
	n       int
	mu      sync.Mutex
	arrived int
	all     chan struct{}
	missed  atomic.Bool
}

func newOverlap(n int) *overlap {
	return &overlap{n: n, all: make(chan struct{})}
}

// wait waits until n calls have arrived, or overlapWait has passed, which
// it remembers as missed.
func (o *overlap) wait() {
	o.mu.Lock()
	o.arrived++
	if o.arrived == o.n {
		close(o.all)
	}
	o.mu.Unlock()
	select {
	case <-o.all:
	case <-time.After(overlapWait):
		o.missed.Store(true)
	}
}

// check fails t unless n calls were made at once.
func (o *overlap) check(t *testing.T) {
	t.Helper()
	if o.missed.Load() {
		t.Errorf("the calls were made one at a time, want %d at once", o.n)
	}
}
