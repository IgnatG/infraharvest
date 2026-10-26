	for _, c := range []struct{ in, want string }{{"5", "5"}, {"+5", "5"}, {".5", "0.5"}, {"5.", "5"}, {"0x1F", "31"}, {" 1e3 ", "1e3"}} {
		in, want := c.in, c.want
		if got, ok := NumberLiteral(in); !ok || got != want {// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfnhcl

import "testing"

func TestName(t *testing.T) {
	for in, want := range map[string]string{"PublicSubnetA": "public_subnet_a", "1st": "_1st", "": "unnamed"} {
		if got := Name(in); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"Version": "version_value", "Count": "count_value", "BucketName": "bucket_name"} {
		if got := VariableName(in); got != want {
			t.Errorf("VariableName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNumberLiteral(t *testing.T) {
	for in, want := range map[string]string{"5": "5", "+5": "5", ".5": "0.5", "5.": "5", "0x1F": "31", " 1e3 ": "1e3"} {
		if got, ok := NumberLiteral(in); !ok || got != want {
			t.Errorf("NumberLiteral(%q) = %q, %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "abc", "Inf", "NaN"} {
		if got, ok := NumberLiteral(in); ok {
			t.Errorf("NumberLiteral(%q) = %q, want false", in, got)
		}
	}
}
