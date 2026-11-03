// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package workers

import (
	"errors"
	"sync/atomic"
	"testing"
)

func TestWaitRaisesAWorkersPanicOnTheCaller(t *testing.T) {
	boom := errors.New("boom")
	var done atomic.Int32
	g := New(2)
	for i := range 6 {
		g.Go(func() {
			if i == 3 {
				panic(boom)
			}
			done.Add(1)
		})
	}
	var got any
	func() {
		defer func() { got = recover() }()
		g.Wait()
	}()
	p, ok := got.(*Panic)
	if !ok {
		t.Fatalf("Wait panicked with %#v, want a *Panic", got)
	}
	if !errors.Is(p, boom) {
		t.Errorf("the Panic doesn't unwrap to the worker's error: %v", p)
	}
	if done.Load() != 5 {
		t.Errorf("%d of the other 5 workers finished, want all", done.Load())
	}
}

func TestWaitWithoutAPanicReturns(t *testing.T) {
	var n atomic.Int32
	var g Group
	for range 5 {
		g.Go(func() { n.Add(1) })
	}
	g.Wait()
	if n.Load() != 5 {
		t.Errorf("%d workers ran, want 5", n.Load())
	}
}
