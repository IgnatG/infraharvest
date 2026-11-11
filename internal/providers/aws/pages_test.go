// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// fakePager pages through pages, failing where errs says, and counts what
// it is asked for.
type fakePager struct {
	pages []int
	errs  map[int]error
	next  int
	asked int
}

type fakeOptions struct{}

func (f *fakePager) HasMorePages() bool { return f.next < len(f.pages) }

func (f *fakePager) NextPage(context.Context, ...func(*fakeOptions)) (int, error) {
	f.asked++
	if err := f.errs[f.next]; err != nil {
		delete(f.errs, f.next) // asked again, the page comes
		return 0, err
	}
	f.next++
	return f.pages[f.next-1], nil
}

// pagesOf walks the pages as the loop it replaces did: every page, in
// order; a loop that stops stops the asking; one that goes on after an
// error asks for the same page again.
func TestPagesOf(t *testing.T) {
	t.Parallel()
	var got []int
	for page, err := range pagesOf(t.Context(), &fakePager{pages: []int{1, 2, 3}}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page)
	}
	if !slices.Equal(got, []int{1, 2, 3}) {
		t.Errorf("pages %v", got)
	}

	failing := &fakePager{pages: []int{1, 2, 3}, errs: map[int]error{1: errors.New("throttled")}}
	for _, err := range pagesOf(t.Context(), failing) {
		if err != nil {
			break
		}
	}
	if failing.asked != 2 {
		t.Errorf("asked %d times, want 2: a loop that returns on an error stops the asking", failing.asked)
	}

	again := &fakePager{pages: []int{1, 2}, errs: map[int]error{1: errors.New("throttled")}}
	got = nil
	for page, err := range pagesOf(t.Context(), again) {
		if err != nil {
			continue
		}
		got = append(got, page)
	}
	if !slices.Equal(got, []int{1, 2}) || again.asked != 3 {
		t.Errorf("pages %v asked %d times, want [1 2] asked 3 times", got, again.asked)
	}
}
