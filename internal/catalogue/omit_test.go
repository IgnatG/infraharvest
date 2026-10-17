// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package catalogue

import (
	"slices"
	"testing"
)

// Entries with a condition on what the lister recorded aren't the type's:
// ResourceOmit gives them for a resource whose attributes meet them,
// without those conditions, keeping the others.
func TestResourceOmit(t *testing.T) {
	t.Parallel()
	catalogues, err := load(t, map[string]string{"x.yaml": `provider: aws
types:
  aws_x:
    omit:
      - a
      - "b when @mode=default"
      - "c when d and @mode=default"
      - "e when !@mode"
`})
	if err != nil {
		t.Fatal(err)
	}
	c := catalogues["aws"]
	if got := c.Omit()["aws_x"]; !slices.Equal(got, []string{"a"}) {
		t.Errorf("Omit: got %v, want [a]", got)
	}
	for _, tc := range []struct {
		attributes map[string]string
		want       []string
	}{
		{map[string]string{"mode": "default"}, []string{"b", "c when d"}},
		{map[string]string{"mode": "custom"}, nil},
		{nil, []string{"e"}},
	} {
		if got := c.ResourceOmit("aws_x", tc.attributes); !slices.Equal(got, tc.want) {
			t.Errorf("ResourceOmit(%v): got %v, want %v", tc.attributes, got, tc.want)
		}
	}
	if got := c.ResourceOmit("aws_y", map[string]string{"mode": "default"}); got != nil {
		t.Errorf("ResourceOmit of a type without an entry: got %v", got)
	}
}
