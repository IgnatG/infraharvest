// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build ignore

// The generator's parts that read no provider: the file's format and how it
// is written. They sit apart from main.go, which imports the provider's SDK
// and framework that infraharvest's module doesn't have, so that meta_test.go
// can test them (see there for how it runs).
package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// file is what the JSON holds: the format schemameta.File reads.
type file struct {
	Provider  string                `json:"provider"`
	Version   string                `json:"version"`
	Resources map[string]*typeEntry `json:"resources"`
}

// typeEntry is one resource type's arguments that have metadata, by their
// path through nested blocks without indexes (bgp.asn).
type typeEntry struct {
	Framework bool                 `json:"framework,omitempty"`
	Arguments map[string]*argument `json:"arguments,omitempty"`
}

// argument is one argument's metadata. Default is a bool, a number or a
// string; the constraints name other arguments by their paths.
type argument struct {
	Default       any      `json:"default,omitempty"`
	Suppress      bool     `json:"suppress,omitempty"`
	ExactlyOneOf  []string `json:"exactly_one_of,omitempty"`
	ConflictsWith []string `json:"conflicts_with,omitempty"`
	AtLeastOneOf  []string `json:"at_least_one_of,omitempty"`
	RequiredWith  []string `json:"required_with,omitempty"`
}

func (a *argument) empty() bool {
	return a.Default == nil && len(a.ExactlyOneOf)+len(a.ConflictsWith)+len(a.AtLeastOneOf)+len(a.RequiredWith) == 0
}

// withoutIndexes returns keys, such as network_interface.0.subnet_id,
// without their list indexes (network_interface.subnet_id), sorted, as
// configuration paths are.
func withoutIndexes(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	paths := make([]string, 0, len(keys))
	for _, key := range keys {
		var kept []string
		for _, segment := range strings.Split(key, ".") {
			if _, err := strconv.Atoi(segment); err != nil {
				kept = append(kept, segment)
			}
		}
		paths = append(paths, strings.Join(kept, "."))
	}
	sort.Strings(paths)
	return slices.Compact(paths)
}

// primitive returns v, a schema's Default, as a JSON bool, number or
// string. Providers use named types for some, such as an SDK's enum of
// strings, so it goes by kind.
func primitive(v any) (any, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Bool:
		return rv.Bool(), true
	case reflect.String:
		return rv.String(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint(), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	}
	return nil, false
}

// encode writes f as compact JSON with one resource type to a line, in
// order, so that a new release's file differs from the last one's only
// in the lines of the types that changed.
func encode(f *file) ([]byte, error) {
	var b bytes.Buffer
	header, err := json.Marshal(struct {
		Provider string `json:"provider"`
		Version  string `json:"version"`
	}{f.Provider, f.Version})
	if err != nil {
		return nil, err
	}
	b.Write(header[:len(header)-1])
	b.WriteString(`,"resources":{`)
	types := make([]string, 0, len(f.Resources))
	for typ := range f.Resources {
		types = append(types, typ)
	}
	sort.Strings(types)
	for i, typ := range types {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
		key, err := json.Marshal(typ)
		if err != nil {
			return nil, err
		}
		// encoding/json writes map keys in order.
		value, err := json.Marshal(f.Resources[typ])
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteString("\n}}\n")
	return b.Bytes(), nil
}
