// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"strings"
	"testing"
)

// Parsing any --id, --tag or --where doesn't panic, a filter that parses
// has the shape its flag promises, and applying it to a resource doesn't
// panic either.
func FuzzParseFilters(f *testing.F) {
	for _, seed := range []string{
		"vpc=vpc-1", "vpc=vpc-1,vpc-2", "vpc=", "vpc=,", "=vpc-1", "vpc",
		"compute_network=projects/p/global/networks/default",
		"env=prod", "env", "env=", "env=prod,staging", "Name=a=b", "=prod",
		"tags.env=prod", "labels.team", "id=vpc-1", "id", "tags.%=2", "a..b=c", ".",
		"", ",", "=", "==", "ключ=значение",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		resource := Resource{
			Address:  &Address{Type: "aws_vpc", ID: "vpc-1"},
			Observed: &Observation{ID: "vpc-1", Attributes: map[string]string{"tags.env": "prod", "tags.%": "1", "cidr_block": "10.0.0.0/16"}},
			Provider: "aws",
		}
		if id, err := ParseIDFilter(raw); err == nil {
			if id.Service == "" || id.Path != idPath || len(id.Values) == 0 {
				t.Errorf("ParseIDFilter(%q) = %+v", raw, id)
			}
			for _, v := range id.Values {
				if v == "" {
					t.Errorf("ParseIDFilter(%q) kept an empty ID: %+v", raw, id)
				}
			}
			_ = id.Keeps(resource)
		}
		if tag, err := ParseTagFilter(raw); err == nil {
			if !strings.HasPrefix(tag.Path, tagPathPrefix) || tag.Path == tagPathPrefix || tag.Service != "" {
				t.Errorf("ParseTagFilter(%q) = %+v", raw, tag)
			}
			_ = tag.Keeps(resource)
		}
		if where, err := ParseWhereFilter(raw); err == nil {
			if where.Path == "" || where.Path == idPath || where.Service != "" {
				t.Errorf("ParseWhereFilter(%q) = %+v", raw, where)
			}
			_ = where.Keeps(resource)
		}
	})
}
