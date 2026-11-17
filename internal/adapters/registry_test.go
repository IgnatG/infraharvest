// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"reflect"
	"slices"
	"testing"
)

// What For and the functions named after the adapters return is a copy:
// changing it, its slices or its maps, changes no adapter the engine
// tries next.
func TestRegistryReturnsCopies(t *testing.T) {
	t.Parallel()
	for _, get := range []struct {
		name string
		get  func() Adapter
	}{
		{"For", func() Adapter { return Builtin().For("aws")[1] }},
		{"IAMRole", IAMRole},
	} {
		t.Run(get.name, func(t *testing.T) {
			t.Parallel()
			a := get.get()
			a.Version = "0.0.1"
			a.Members[0] = "changed"
			a.Inverse["aws_iam_role.this.tags"] = "changed"
			delete(a.Defaults, "path")
			got := IAMRole()
			if _, ok := got.Defaults["path"]; !ok || got.Version == "0.0.1" || got.Members[0] == "changed" || got.Inverse["aws_iam_role.this.tags"] == "changed" {
				t.Errorf("changing a copy changed the adapter: %+v", got)
			}
			list := Builtin().For("aws")
			list[0] = Adapter{}
			if Builtin().For("aws")[0].Source == "" {
				t.Error("changing For's list changed the family's")
			}
		})
	}
}

// clone gives the copy every slice and map of its own, the fields added
// later among them.
func TestCloneCopiesEverySliceAndMap(t *testing.T) {
	t.Parallel()
	for _, family := range Builtin().Providers() {
		for _, a := range Builtin().byFamily[family] {
			c := a.clone()
			av, cv := reflect.ValueOf(a), reflect.ValueOf(c)
			for i := range av.NumField() {
				f, g := av.Field(i), cv.Field(i)
				if (f.Kind() != reflect.Slice && f.Kind() != reflect.Map) || f.Len() == 0 {
					continue
				}
				if f.UnsafePointer() == g.UnsafePointer() {
					t.Errorf("%s: the clone shares %s", a.Source, av.Type().Field(i).Name)
				}
			}
		}
	}
}

// Each registry is its own: adding to or replacing the adapters of one
// leaves the built-in definitions, and every other registry, as they are.
func TestRegistriesAreIndependent(t *testing.T) {
	t.Parallel()
	a, b := Builtin(), Builtin()
	wantAWS := len(b.For("aws"))
	own := Adapter{Source: "example.com/own/module", Anchor: "aws_sqs_queue", Members: []string{"aws_sqs_queue_policy"}}

	a.Add("aws", own)
	a.Replace("azure", own)
	a.Replace("google")
	a.Add("custom", own)

	if got := a.For("aws"); len(got) != wantAWS+1 || got[wantAWS].Source != own.Source {
		t.Errorf("a: aws has %d adapters, the last %q; want %d, the added one", len(got), got[len(got)-1].Source, wantAWS+1)
	}
	if got := a.For("azure"); len(got) != 1 || got[0].Source != own.Source {
		t.Errorf("a: azure = %+v, want the replacement alone", got)
	}
	if got := a.For("google"); got != nil {
		t.Errorf("a: google = %d adapters, want none after Replace with none", len(got))
	}
	for _, other := range []*Registry{b, Builtin()} {
		if got := other.For("aws"); len(got) != wantAWS {
			t.Errorf("another registry: aws has %d adapters, want %d", len(got), wantAWS)
		}
		if got := other.For("azure"); len(got) != 1 || got[0].Source == own.Source {
			t.Errorf("another registry: azure = %+v, want the built-in one", got)
		}
		if len(other.For("google")) == 0 {
			t.Error("another registry lost google's adapters")
		}
		if other.For("custom") != nil {
			t.Error("another registry has the custom family")
		}
	}
	if got := a.Providers(); !slices.Equal(got, []string{"aws", "azure", "custom"}) {
		t.Errorf("a: providers %v", got)
	}
	if got := b.Providers(); !slices.Equal(got, []string{"aws", "azure", "google"}) {
		t.Errorf("b: providers %v", got)
	}
}

// What a caller hands Add or Replace is copied, and so is what a registry
// is cloned from.
func TestRegistryCopiesWhatItIsGiven(t *testing.T) {
	t.Parallel()
	own := Adapter{Source: "example.com/own/module", Members: []string{"a"}}
	r := NewRegistry()
	r.Add("own", own)
	own.Members[0] = "changed"
	if got := r.For("own")[0].Members[0]; got != "a" {
		t.Errorf("changing the adapter after Add changed the registry's: %q", got)
	}
	c := r.Clone()
	c.Replace("own")
	if len(r.For("own")) != 1 {
		t.Error("changing a clone changed the registry")
	}
	var none *Registry
	if none.For("aws") != nil || none.Providers() != nil || len(none.Clone().Providers()) != 0 {
		t.Error("a nil registry has adapters")
	}
	if len(NewRegistry().Providers()) != 0 {
		t.Error("a new registry has families")
	}
}
