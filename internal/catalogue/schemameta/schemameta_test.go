// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package schemameta

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/go-version"
)

// Every built-in file parses, is of the provider it is named after, at a
// release, and names in its constraints only arguments it could hold.
func TestBuiltin(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"aws", "azurerm", "google"} {
		f, err := Builtin(provider)
		if err != nil {
			t.Fatal(err)
		}
		if f == nil {
			t.Fatalf("no built-in metadata for %s", provider)
		}
		if want := "registry.terraform.io/hashicorp/" + provider; f.Provider != want {
			t.Errorf("%s.json: provider %q, want %q", provider, f.Provider, want)
		}
		if _, err := version.NewSemver(f.Version); err != nil {
			t.Errorf("%s.json: version %q: %v", provider, f.Version, err)
		}
		if len(f.Resources) == 0 {
			t.Errorf("%s.json: no resource types", provider)
		}
		t.Logf("%s %s: %d resource types", provider, f.Version, len(f.Resources))
		for typ, r := range f.Resources {
			if !strings.HasPrefix(typ, provider+"_") {
				t.Errorf("%s.json: type %s isn't the provider's", provider, typ)
			}
			for path, a := range r.Arguments {
				if r.Framework && len(a.ExactlyOneOf)+len(a.ConflictsWith)+len(a.AtLeastOneOf)+len(a.RequiredWith) > 0 {
					t.Errorf("%s: %s: a Plugin Framework resource with constraints", typ, path)
				}
				for _, list := range [][]string{a.ExactlyOneOf, a.ConflictsWith, a.AtLeastOneOf, a.RequiredWith} {
					for _, other := range list {
						if other == "" || strings.Contains(other, "..") {
							t.Errorf("%s: %s: constraint names %q", typ, path, other)
						}
					}
				}
			}
		}
	}
	if f, err := Builtin("kubernetes"); err != nil || f != nil {
		t.Errorf("Builtin(kubernetes) = %v, %v, want none", f, err)
	}
}

// The AWS provider's own code says what the engine's tests assume of it.
func TestBuiltinAWSKnowsSubnetZones(t *testing.T) {
	t.Parallel()
	f, err := Builtin("aws")
	if err != nil {
		t.Fatal(err)
	}
	a := f.Argument("aws_subnet", "availability_zone")
	if a == nil || !slices.Contains(a.ConflictsWith, "availability_zone_id") {
		t.Errorf("aws_subnet.availability_zone: got %+v, want it to conflict with availability_zone_id", a)
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	f, err := Parse([]byte(`{"provider":"registry.terraform.io/hashicorp/aws","version":"6.70.0","resources":{
"aws_thing":{"arguments":{"retries":{"default":3},"ratio":{"default":0.25},"mode":{"default":"auto","suppress":true},"on":{"default":false},"a":{"conflicts_with":["b"]}}},
"aws_fw":{"framework":true}
}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Argument("aws_thing", "retries").Default; got != json.Number("3") {
		t.Errorf("retries: default %#v, want json.Number 3", got)
	}
	if got := f.Argument("aws_thing", "ratio").Default; got != json.Number("0.25") {
		t.Errorf("ratio: default %#v", got)
	}
	if a := f.Argument("aws_thing", "mode"); a.Default != "auto" || !a.Suppress {
		t.Errorf("mode: %+v", a)
	}
	if got := f.Argument("aws_thing", "on").Default; got != false {
		t.Errorf("on: default %#v, want false", got)
	}
	if r := f.Resource("aws_fw"); r == nil || !r.Framework {
		t.Errorf("aws_fw: %+v", r)
	}
	if f.Argument("aws_other", "x") != nil || (*File)(nil).Argument("aws_thing", "retries") != nil {
		t.Error("an argument without metadata has some")
	}

	for _, bad := range []string{
		`{"provider":"p","version":"1.0.0","resources":{"t":{"arguments":{"x":{"default":[1]}}}}}`,
		`{"provider":"p","version":"1.0.0","resources":{"t":{"arguments":{"x":{"defualt":1}}}}}`,
		`{"provider":"p","resources":{}}`,
		`{"provider":"p","version":"1.0.0","resources":{"t":null}}`,
		`{"provider":"p","version":"1.0.0","resources":{"t":{"arguments":{"x":null}}}}`,
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%s): want an error", bad)
		}
	}
}
