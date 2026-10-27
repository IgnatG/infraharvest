// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package adapters

import (
	"reflect"
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
		{"For", func() Adapter { return For("aws")[1] }},
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
			list := For("aws")
			list[0] = Adapter{}
			if For("aws")[0].Source == "" {
				t.Error("changing For's list changed the family's")
			}
		})
	}
}

// clone gives the copy every slice and map of its own, the fields added
// later among them.
func TestCloneCopiesEverySliceAndMap(t *testing.T) {
	t.Parallel()
	for _, family := range Providers() {
		for _, a := range byFamily[family] {
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
