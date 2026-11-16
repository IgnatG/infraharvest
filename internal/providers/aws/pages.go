// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"iter"
)

// pager is what every SDK paginator offers: O is its page, Options its
// client's options.
type pager[O, Options any] interface {
	HasMorePages() bool
	NextPage(ctx context.Context, optFns ...func(*Options)) (O, error)
}

// pagesOf yields the pages of p, each with the error of asking for it, as
//
//	for p.HasMorePages() {
//		page, err := p.NextPage(ctx)
//		...
//	}
//
// walks them, and the same way: the loop decides what an error does, and
// a loop that goes on after one asks p for the next page again. p is made
// with stopOnDuplicateToken, as every paginator of the provider is (see
// TestEveryPaginatorStopsOnDuplicateToken).
func pagesOf[O, Options any](ctx context.Context, p pager[O, Options]) iter.Seq2[O, error] {
	return func(yield func(O, error) bool) {
		for p.HasMorePages() {
			if !yield(p.NextPage(ctx)) {
				return
			}
		}
	}
}
