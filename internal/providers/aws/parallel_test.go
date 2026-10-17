// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"sync"
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
