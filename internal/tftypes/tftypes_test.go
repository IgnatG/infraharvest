// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tftypes

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"
)

// The types of a group are other names of each other, and a type of no
// group has none.
func TestAliases(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		typ  string
		want []string
	}{
		{"aws_alb", []string{"aws_lb"}},
		{"aws_lb", []string{"aws_alb"}},
		{"azurerm_virtual_machine", []string{"azurerm_linux_virtual_machine", "azurerm_windows_virtual_machine"}},
		{"azurerm_linux_virtual_machine", []string{"azurerm_virtual_machine", "azurerm_windows_virtual_machine"}},
		{"aws_sqs_queue", nil},
	} {
		if got := Aliases(tt.typ); !slices.Equal(got, tt.want) {
			t.Errorf("%s: got %q, want %q", tt.typ, got, tt.want)
		}
	}
	if !Same("aws_alb_listener", "aws_lb_listener") || !Same("aws_sqs_queue", "aws_sqs_queue") || Same("aws_alb", "aws_lb_listener") {
		t.Error("Same doesn't match the groups")
	}
	if got := Canonical("google_compute_instance_from_template"); got != "google_compute_instance" {
		t.Errorf("Canonical: got %s", got)
	}
}

// An identity's ID lists its attributes sorted by key, whatever order
// they come in, and its values read from JSON keep their meaning.
func TestIdentityID(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		identity map[string]any
		want     string
		ok       bool
	}{
		{map[string]any{"region": "eu-west-2", "name": "/app/logs", "account_id": "123456789012"}, "account_id=123456789012,name=/app/logs,region=eu-west-2", true},
		{map[string]any{"name": "web", "port": float64(443), "global": true, "zone": nil}, "global=true,name=web,port=443", true},
		{map[string]any{"name": "web", "port": json.Number("443"), "weight": json.Number("1.50")}, "name=web,port=443,weight=1.5", true},
		{map[string]any{}, "", true},
		{map[string]any{"names": []any{"a"}}, "", false},
	} {
		values, ok := IdentityValues(tt.identity)
		if ok != tt.ok {
			t.Errorf("%v: ok %v, want %v", tt.identity, ok, tt.ok)
			continue
		}
		if got := IdentityID(values); got != tt.want {
			t.Errorf("%v: got %q, want %q", tt.identity, got, tt.want)
		}
	}
}

// An identity's ID is the same as it always was where no attribute has a
// backslash or a comma, or a key an equals sign, and tells every other
// identity apart.
func TestIdentityIDEscapes(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		identity map[string]string
		want     string
	}{
		{nil, ""},
		{map[string]string{"name": "a/b", "region": "eu-west-2"}, "name=a/b,region=eu-west-2"},
		{map[string]string{"name": "a=b"}, "name=a=b"},
		{map[string]string{"name": "a,region=b"}, `name=a\,region=b`},
		{map[string]string{"name": "a", "region": "b"}, "name=a,region=b"},
		{map[string]string{`a\`: "b"}, `a\\=b`},
		{map[string]string{"a=b": "c"}, `a\=b=c`},
		{map[string]string{"a,b": "c"}, `a\,b=c`},
		{map[string]string{`n`: `c:\dir\`}, `n=c:\\dir\\`},
		{map[string]string{"": ""}, "="},
		{map[string]string{"a": "", "b": ""}, "a=,b="},
	} {
		got := IdentityID(tt.identity)
		if got != tt.want {
			t.Errorf("%q: got %q, want %q", tt.identity, got, tt.want)
		}
		if back, ok := ParseIdentityID(got); !ok || !maps.Equal(back, nonNil(tt.identity)) {
			t.Errorf("%q: parsed %q back as %q, %v", tt.identity, got, back, ok)
		}
	}
}

// Identities that the old, unescaped ID wrote the same are told apart.
func TestIdentityIDInjective(t *testing.T) {
	t.Parallel()
	a := map[string]string{"name": "a,region=b", "z": "1"}
	b := map[string]string{"name": "a", "region": "b,z=1"}
	c := map[string]string{"name": "a", "region": "b", "z": "1"}
	seen := map[string]map[string]string{}
	for _, id := range []map[string]string{a, b, c} {
		got := IdentityID(id)
		if other, dup := seen[got]; dup {
			t.Errorf("%q and %q both have the ID %q", id, other, got)
		}
		seen[got] = id
	}
}

// Text IdentityID doesn't make isn't an ID.
func TestParseIdentityIDRefuses(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"a", "a=b,", ",a=b", "a=b,,c=d", `a=b\`, `a=\x`, `a\x=b`, "b=1,a=2", "a=1,a=2", `a=b\=`} {
		if got, ok := ParseIdentityID(id); ok {
			t.Errorf("%q: parsed as %q", id, got)
		}
	}
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// Any identity's ID reads back as the identity, so no two share one.
func FuzzIdentityID(f *testing.F) {
	for _, seed := range [][4]string{{"name", "a", "region", "b"}, {"a=b", "c,d", `e\`, "f"}, {"", "", "x", "="}, {"k", `\,`, "k2", `,\=`}} {
		f.Add(seed[0], seed[1], seed[2], seed[3])
	}
	f.Fuzz(func(t *testing.T, k1, v1, k2, v2 string) {
		identity := map[string]string{k1: v1, k2: v2}
		id := IdentityID(identity)
		back, ok := ParseIdentityID(id)
		if !ok || !maps.Equal(back, identity) {
			t.Fatalf("%q: ID %q read back as %q, %v", identity, id, back, ok)
		}
		if IdentityID(back) != id {
			t.Fatalf("%q: ID %q changed on the way back", identity, id)
		}
	})
}
