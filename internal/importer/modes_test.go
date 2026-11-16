// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import "testing"

func TestRootsMode(t *testing.T) {
	for _, tt := range []struct {
		options Options
		roots   RootsMode
		reads   bool
	}{
		{Options{}, RootsFresh, false},
		{Options{Resume: true}, RootsFresh, true},
		{Options{Regenerate: true}, RootsRegenerate, true},
		{Options{Regenerate: true, Resume: true}, RootsRegenerate, true},
		{Options{Incremental: true}, RootsIncremental, true},
	} {
		if got := tt.options.Roots(); got != tt.roots {
			t.Errorf("%+v: Roots() = %d, want %d", tt.options, got, tt.roots)
		}
		if got := tt.options.readsEarlierRoots(); got != tt.reads {
			t.Errorf("%+v: readsEarlierRoots() = %v, want %v", tt.options, got, tt.reads)
		}
	}
	if err := checkModes(Options{All: true, Regenerate: true, Incremental: true}); err == nil {
		t.Error("checkModes passed --regenerate with --incremental")
	}
}
