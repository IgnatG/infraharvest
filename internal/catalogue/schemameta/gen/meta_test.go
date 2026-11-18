// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build ignore

// Tests the generator's parts that read no provider (meta.go). Like the
// generator, they carry the ignore constraint, as main.go can't be built
// without a provider's SDK, so go test takes the two files by name:
//
//	go test ./internal/catalogue/schemameta/gen/meta.go ./internal/catalogue/schemameta/gen/meta_test.go
package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/internal/catalogue/schemameta"
)

type enum string

func TestWithoutIndexes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		keys []string
		want []string
	}{
		{"none", nil, nil},
		{"empty", []string{}, nil},
		{"plain", []string{"b", "a"}, []string{"a", "b"}},
		{"indexes dropped", []string{"network_interface.0.subnet_id"}, []string{"network_interface.subnet_id"}},
		{"nested indexes", []string{"a.0.b.12.c"}, []string{"a.b.c"}},
		{"sorted after dropping", []string{"z", "a.0.b", "a.b"}, []string{"a.b", "z"}},
		{"duplicates after dropping", []string{"a.0.b", "a.1.b"}, []string{"a.b"}},
		{"a name that only starts with digits stays", []string{"a.0x.b", "1a"}, []string{"1a", "a.0x.b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := withoutIndexes(tt.keys); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("withoutIndexes(%q) = %q, want %q", tt.keys, got, tt.want)
			}
		})
	}
}

func TestPrimitive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   any
		want any
		ok   bool
	}{
		{"bool", true, true, true},
		{"string", "x", "x", true},
		{"named string", enum("gp3"), "gp3", true},
		{"int", 5, int64(5), true},
		{"int32", int32(-3), int64(-3), true},
		{"uint8", uint8(7), uint64(7), true},
		{"float", 1.5, 1.5, true},
		{"slice", []string{"a"}, nil, false},
		{"map", map[string]string{}, nil, false},
		{"struct", struct{}{}, nil, false},
		{"nil", nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := primitive(tt.in)
			if ok != tt.ok || got != tt.want {
				t.Errorf("primitive(%#v) = %#v, %v, want %#v, %v", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestArgumentEmpty(t *testing.T) {
	t.Parallel()
	for name, a := range map[string]*argument{
		"nothing":       {},
		"only suppress": {Suppress: true},
	} {
		if !a.empty() {
			t.Errorf("%s: not empty", name)
		}
	}
	for name, a := range map[string]*argument{
		"default":        {Default: false},
		"default string": {Default: ""},
		"exactly one of": {ExactlyOneOf: []string{"a"}},
		"conflicts with": {ConflictsWith: []string{"a"}},
		"at least one":   {AtLeastOneOf: []string{"a"}},
		"required with":  {RequiredWith: []string{"a"}},
	} {
		if a.empty() {
			t.Errorf("%s: empty", name)
		}
	}
}

// What encode writes is what schemameta reads, with the resource types in
// order and one to a line, so that a release's file differs from the last
// one's only in the types that changed.
func TestEncode(t *testing.T) {
	t.Parallel()
	f := &file{
		Provider: "registry.terraform.io/hashicorp/aws",
		Version:  "6.70.0",
		Resources: map[string]*typeEntry{
			"aws_b": {Framework: true, Arguments: map[string]*argument{"x": {Default: int64(3)}}},
			"aws_a": {Arguments: map[string]*argument{
				"enabled": {Default: true},
				"mode":    {Default: "STANDARD", Suppress: true},
				"block.a": {ExactlyOneOf: []string{"block.b", "block.c"}, ConflictsWith: []string{"z"}},
			}},
			"aws_c": {Framework: true},
		},
	}
	got, err := encode(f)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("%d lines, want a header, three types and a footer:\n%s", len(lines), got)
	}
	for i, prefix := range []string{`{"provider":"registry.terraform.io/hashicorp/aws","version":"6.70.0","resources":{`, `"aws_a":`, `"aws_b":`, `"aws_c":`, `}}`} {
		if !strings.HasPrefix(lines[i], prefix) {
			t.Errorf("line %d is %q, want it to start %q", i+1, lines[i], prefix)
		}
	}
	for _, line := range lines[1:3] {
		if !strings.HasSuffix(line, "}") && !strings.HasSuffix(line, "},") {
			t.Errorf("a type's line doesn't end its object: %q", line)
		}
	}
	if !strings.HasSuffix(lines[3], "}") {
		t.Errorf("the last type's line has a trailing comma: %q", lines[3])
	}
	if !strings.HasSuffix(string(got), "\n") {
		t.Error("no final newline")
	}

	parsed, err := schemameta.Parse(got)
	if err != nil {
		t.Fatalf("schemameta can't read it: %v", err)
	}
	if parsed.Provider != f.Provider || parsed.Version != f.Version || len(parsed.Resources) != 3 {
		t.Errorf("parsed %q %q with %d types", parsed.Provider, parsed.Version, len(parsed.Resources))
	}
	if a := parsed.Argument("aws_a", "mode"); a == nil || a.Default != "STANDARD" || !a.Suppress {
		t.Errorf("aws_a mode = %#v", a)
	}
	if a := parsed.Argument("aws_a", "enabled"); a == nil || a.Default != true {
		t.Errorf("aws_a enabled = %#v", a)
	}
	if a := parsed.Argument("aws_a", "block.a"); a == nil || !reflect.DeepEqual(a.ExactlyOneOf, []string{"block.b", "block.c"}) || !reflect.DeepEqual(a.ConflictsWith, []string{"z"}) {
		t.Errorf("aws_a block.a = %#v", a)
	}
	if a := parsed.Argument("aws_b", "x"); a == nil || a.Default != json.Number("3") {
		t.Errorf("aws_b x = %#v", a)
	}
	if r := parsed.Resource("aws_b"); r == nil || !r.Framework {
		t.Errorf("aws_b = %#v, want a framework type", r)
	}
	if r := parsed.Resource("aws_c"); r == nil || !r.Framework || len(r.Arguments) != 0 {
		t.Errorf("aws_c = %#v, want a framework type without arguments", r)
	}
}

func TestEncodeNoResources(t *testing.T) {
	t.Parallel()
	got, err := encode(&file{Provider: "registry.terraform.io/hashicorp/google", Version: "7.0.0", Resources: map[string]*typeEntry{}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := schemameta.Parse(got)
	if err != nil {
		t.Fatalf("schemameta can't read %q: %v", got, err)
	}
	if len(parsed.Resources) != 0 {
		t.Errorf("%d types, want none", len(parsed.Resources))
	}
}

// The same file always gives the same bytes: the workflow gzips it without
// a name or time, and a release's diff shows only what changed.
func TestEncodeIsDeterministic(t *testing.T) {
	t.Parallel()
	build := func() *file {
		f := &file{Provider: "p", Version: "1.0.0", Resources: map[string]*typeEntry{}}
		for _, name := range []string{"r_e", "r_a", "r_d", "r_b", "r_c"} {
			f.Resources[name] = &typeEntry{Arguments: map[string]*argument{"a": {Default: name}, "b": {RequiredWith: []string{"a"}}}}
		}
		return f
	}
	first, err := encode(build())
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, err := encode(build())
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatalf("encode differs between runs:\n%s\n%s", first, again)
		}
	}
}
