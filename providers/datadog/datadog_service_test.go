// Copyright 2026 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package datadog

import (
	"errors"
	"reflect"
	"testing"

	"github.com/DataDog/datadog-api-client-go/v2/api/datadog"
)

func pages[T any](results ...datadog.PaginationResult[T]) (<-chan datadog.PaginationResult[T], func()) {
	items := make(chan datadog.PaginationResult[T], len(results))
	for _, r := range results {
		items <- r
	}
	close(items)
	return items, func() {}
}

func TestCollectPages(t *testing.T) {
	got, err := collectPages(pages(
		datadog.PaginationResult[string]{Item: "a"},
		datadog.PaginationResult[string]{Item: "b"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCollectPagesError(t *testing.T) {
	cancelled := false
	items, _ := pages(
		datadog.PaginationResult[string]{Item: "a"},
		datadog.PaginationResult[string]{Error: errors.New("boom")},
	)
	if _, err := collectPages(items, func() { cancelled = true }); err == nil {
		t.Fatal("expected an error")
	}
	if !cancelled {
		t.Fatal("collectPages did not cancel the pager")
	}
}
