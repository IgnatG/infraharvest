// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"reflect"
	"testing"
)

func TestValuesAt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		path string
		data interface{}
		want []interface{}
	}{
		{
			name: "empty map",
			path: "attr1",
			data: map[string]interface{}{},
			want: []interface{}{},
		},
		{
			name: "empty typed nested map",
			path: "attr1.attr2",
			data: map[string]map[string]interface{}{},
			want: []interface{}{},
		},
		{
			name: "top-level key",
			path: "attr1",
			data: map[string]interface{}{"attr1": "value"},
			want: []interface{}{"value"},
		},
		{
			// The elements of an array are values of their own.
			name: "typed map of arrays",
			path: "attr1",
			data: map[string][]interface{}{"attr1": {"value"}},
			want: []interface{}{"value"},
		},
		{
			name: "typed nested map",
			path: "attr1.attr2",
			data: map[string]map[string]interface{}{"attr1": {"attr2": "value"}},
			want: []interface{}{"value"},
		},
		{
			// The rest of the path may be a key itself, dots and all.
			name: "dotted key in a nested map",
			path: "attr1.attr2.attr3",
			data: map[string]map[string]interface{}{"attr1": {"attr2.attr3": "value"}},
			want: []interface{}{"value"},
		},
		{
			name: "array of maps",
			path: "attr1.attr2",
			data: map[string]interface{}{"attr1": []interface{}{
				map[string]interface{}{"attr2": "value1"},
				map[string]interface{}{"attr2": "value2"},
			}},
			want: []interface{}{"value1", "value2"},
		},
		{
			name: "path past a leaf",
			path: "attr1.attr2",
			data: map[string]interface{}{"attr1": "test"},
			want: []interface{}{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// DeepEqual tells an empty result from a nil one.
			if got := ValuesAt(tc.path, tc.data); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ValuesAt(%q): got %#v, want %#v", tc.path, got, tc.want)
			}
		})
	}
}

func TestHasPath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		data map[string]interface{}
		want bool
	}{
		{"empty map", map[string]interface{}{}, false},
		{"present", map[string]interface{}{"attr1": "value"}, true},
	} {
		if got := HasPath("attr1", tc.data); got != tc.want {
			t.Errorf("%s: HasPath(attr1) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
