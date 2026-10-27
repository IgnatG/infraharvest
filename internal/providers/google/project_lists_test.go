// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"context"
	"errors"
	"testing"
)

// A project's list that fails is kept, unless its context ended.
func TestProjectListsKeepFailures(t *testing.T) {
	var lists projectLists[int]
	calls := 0
	denied := errors.New("denied")
	for range 2 {
		if _, err := lists.once("acme-prod", func() (int, error) { calls++; return 0, denied }); !errors.Is(err, denied) {
			t.Errorf("error %v, want %v", err, denied)
		}
	}
	if calls != 1 {
		t.Errorf("a failure: listed %d times, want once", calls)
	}
	for range 2 {
		if _, err := lists.once("acme-dev", func() (int, error) { calls++; return 0, context.Canceled }); !errors.Is(err, context.Canceled) {
			t.Errorf("error %v, want %v", err, context.Canceled)
		}
	}
	if calls != 3 {
		t.Errorf("a cancelled list: listed %d times, want twice", calls-1)
	}
}
