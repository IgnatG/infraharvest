// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package harvest

import (
	"reflect"
	"strings"
)

// valuesAt returns the string values found at the dotted path in data,
// which is made of maps with string keys, []interface{} arrays and string
// leaves, as decoded JSON or flattened attributes are. See walkPath for how
// a path is followed. The result is empty, and may be nil, when nothing is
// found.
func valuesAt(path string, data interface{}) []interface{} {
	return walkPath(path, data).values
}

// hasPath reports whether the dotted path leads somewhere in
// data, as valuesAt follows it.
func hasPath(path string, data interface{}) bool {
	return walkPath(path, data).found
}

// walkResult is the outcome of following a path: whether it led somewhere,
// and the string leaves it led to.
type walkResult struct {
	found  bool
	values []interface{}
}

// walkPath follows path, a dotted list of map keys, into data:
//
//   - a nil value is found at the empty path, with no values;
//   - an []interface{} array is walked element by element with the same
//     path, and gives the values of all its elements together; it is found
//     when there are any;
//   - a map with string keys is walked by walkMap;
//   - a string (or a type based on string) at the empty path is found, and
//     is the value itself;
//   - anything else, other kinds of slices included, is not found.
func walkPath(path string, data interface{}) walkResult {
	switch data := data.(type) {
	case nil:
		return walkResult{found: path == "", values: []interface{}{}}
	case []interface{}:
		return walkElements(path, data)
	}

	value := reflect.ValueOf(data)
	switch value.Kind() {
	case reflect.Map:
		if result, ok := walkMap(path, value); ok {
			return result
		}
	case reflect.String:
		if path == "" {
			return walkResult{found: true, values: []interface{}{data}}
		}
	}
	return walkResult{values: []interface{}{}}
}

// walkElements walks every element of an array with path, and collects the
// values of the elements where it was found. The values are nil when there
// are none.
func walkElements(path string, elements []interface{}) walkResult {
	var values []interface{}
	for _, element := range elements {
		if result := walkPath(path, element); result.found {
			values = append(values, result.values...)
		}
	}
	return walkResult{found: len(values) > 0, values: values}
}

// walkMap follows path into a map value. It returns false when no key of
// the map matches.
//
// A key equal to the path's first segment is followed with the rest of the
// path. If that leads nowhere, the whole path is tried again one level
// down, so that a segment repeated at the start of the data, as in a.b on
// {"a": {"a": {"b": ...}}}, may still be followed. Otherwise a key equal to
// the whole path, dots included, leads to its value, as tags.Name does in
// flattened attributes.
func walkMap(path string, m reflect.Value) (walkResult, bool) {
	if m.Type().Key().Kind() != reflect.String {
		return walkResult{}, false
	}
	first, rest, _ := strings.Cut(path, ".")
	if child, ok := mapValue(m, first); ok {
		if result := walkPath(rest, child); result.found {
			return result, true
		}
		return walkPath(path, child), true
	}
	if child, ok := mapValue(m, path); ok {
		return walkPath("", child), true
	}
	return walkResult{}, false
}

// mapValue returns the value at key in m, a map whose keys are of a string
// kind, and whether the key is present.
func mapValue(m reflect.Value, key string) (interface{}, bool) {
	value := m.MapIndex(reflect.ValueOf(key).Convert(m.Type().Key()))
	if !value.IsValid() {
		return nil, false
	}
	return value.Interface(), true
}
