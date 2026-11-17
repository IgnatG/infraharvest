// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import "testing"

func TestRootsMode(t *testing.T) {
	for _, tt := range []struct {
		options Options
		reads   bool
	}{
		{Options{}, false},
		{Options{Resume: true}, true},
		{Options{Roots: RootsRegenerate}, true},
		{Options{Roots: RootsRegenerate, Resume: true}, true},
		{Options{Roots: RootsIncremental}, true},
	} {
		if got := tt.options.readsEarlierRoots(); got != tt.reads {
			t.Errorf("%+v: readsEarlierRoots() = %v, want %v", tt.options, got, tt.reads)
		}
	}
}

func TestModeString(t *testing.T) {
	for want, got := range map[string]string{
		"selection":   ModeSelection.String(),
		"all":         ModeAll.String(),
		"discover":    ModeDiscover.String(),
		"":            RootsFresh.String(),
		"regenerate":  RootsRegenerate.String(),
		"incremental": RootsIncremental.String(),
	} {
		if got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}
