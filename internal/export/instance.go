// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"fmt"
	"reflect"
	"strings"
)

// instanceOf returns v, a document, as encoding/json would decode its
// encoding into an any: objects as map[string]any, arrays as []any, numbers
// as float64, and nil for what encodes as null. The schema validator takes
// that form, not Go structs; building it directly saves encoding and
// decoding a document of every resource only to validate it. It knows the
// kinds the documents use, and returns an error for any other, so that a
// document type it can't follow fails rather than goes unchecked. A test
// checks it against encoding/json.
func instanceOf(v any) (any, error) {
	return instance(reflect.ValueOf(v))
}

func instance(v reflect.Value) (any, error) {
	switch v.Kind() {
	case reflect.Invalid:
		return nil, nil
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil, nil
		}
		return instance(v.Elem())
	case reflect.String:
		return v.String(), nil
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint()), nil
	case reflect.Slice:
		if v.IsNil() {
			return nil, nil
		}
		items := make([]any, v.Len())
		for i := range items {
			item, err := instance(v.Index(i))
			if err != nil {
				return nil, err
			}
			items[i] = item
		}
		return items, nil
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("a map keyed by %s", v.Type().Key())
		}
		if v.IsNil() {
			return nil, nil
		}
		m := make(map[string]any, v.Len())
		for it := v.MapRange(); it.Next(); {
			value, err := instance(it.Value())
			if err != nil {
				return nil, err
			}
			m[it.Key().String()] = value
		}
		return m, nil
	case reflect.Struct:
		return structInstance(v, nil, nil)
	}
	return nil, fmt.Errorf("a value of kind %s", v.Kind())
}

// splitInstance is instanceOf of v, a pointer to a document's struct, but
// each array that names lists, and that isn't null, is empty in it: those
// are returned as they are, by name, for the caller to take an item at a
// time.
func splitInstance(v any, names map[string]bool) (map[string]any, map[string]reflect.Value, error) {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer && !rv.IsNil() {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, nil, fmt.Errorf("a document of kind %s", rv.Kind())
	}
	split := map[string]reflect.Value{}
	m, err := structInstance(rv, names, split)
	if err != nil {
		return nil, nil, err
	}
	return m.(map[string]any), split, nil
}

// structInstance is instance of v, a struct. A field that names names and
// that is a slice that isn't nil is an empty array in it, and goes into
// split as it is.
func structInstance(v reflect.Value, names map[string]bool, split map[string]reflect.Value) (any, error) {
	t := v.Type()
	m := make(map[string]any, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		// encoding/json lifts an embedded struct's fields, exported or not:
		// the documents embed none.
		if f.Anonymous {
			return nil, fmt.Errorf("the embedded field %s of %s", f.Name, t)
		}
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, options, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		for _, o := range strings.Split(options, ",") {
			if o != "" && o != "omitempty" {
				return nil, fmt.Errorf("the json option %q of %s.%s", o, t, f.Name)
			}
		}
		fv := v.Field(i)
		if strings.Contains(","+options+",", ",omitempty,") && emptyValue(fv) {
			continue
		}
		if names[name] && fv.Kind() == reflect.Slice && !fv.IsNil() {
			split[name] = fv
			m[name] = []any{}
			continue
		}
		value, err := instance(fv)
		if err != nil {
			return nil, err
		}
		m[name] = value
	}
	return m, nil
}

// emptyValue is encoding/json's test for omitempty.
func emptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	}
	return false
}
