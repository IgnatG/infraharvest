// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"encoding"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var updateKeys = flag.Bool("update", false, "rewrite the golden files of the JSON documents' keys in testdata")

// keyDocuments are the JSON documents others read, with schema_version:
// report.json, which the Action, the MCP server and scripts read; drift.json;
// and what discover --format json prints. Each golden file lists every key
// path the document can hold, with its JSON type, as encoding/json writes
// the Go type: a key the code renames or removes breaks those readers, so
// it fails here. To rewrite the files after an intended change, run the
// remote workflow's golden task on the branch, or locally:
//
//	go test ./internal/report -run TestGoldenDocumentKeys -update
var keyDocuments = []struct {
	name string
	doc  any
}{
	{"report", Report{}},
	{"drift", Drift{}},
	{"discovery", Discovery{}},
}

// TestGoldenDocumentKeys checks the key paths of each document against its
// golden file, testdata/<name>.keys.golden.
func TestGoldenDocumentKeys(t *testing.T) {
	for _, d := range keyDocuments {
		t.Run(d.name, func(t *testing.T) {
			got := strings.Join(documentKeys(reflect.TypeOf(d.doc)), "\n") + "\n"
			path := filepath.Join("testdata", d.name+".keys.golden")
			if *updateKeys {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v: write it with -update", err)
			}
			want := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
			have := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
			var removed, added []string
			for _, k := range want {
				if !slices.Contains(have, k) {
					removed = append(removed, k)
				}
			}
			for _, k := range have {
				if !slices.Contains(want, k) {
					added = append(added, k)
				}
			}
			if len(removed) > 0 {
				t.Errorf("%s.json no longer has these keys, or has them with another type:\n  %s\n"+
					"Readers of the document break: keep the keys, or bump SchemaVersion and say so in the release notes, then rewrite %s with -update.",
					d.name, strings.Join(removed, "\n  "), path)
			}
			if len(added) > 0 {
				t.Errorf("%s.json has new keys:\n  %s\nAdding keys keeps the schema version: rewrite %s with -update.",
					d.name, strings.Join(added, "\n  "), path)
			}
		})
	}
}

// documentKeys returns the key paths of a JSON document of type t, sorted,
// one per line as "path type", with " omitempty" for keys left out when
// empty. Arrays are path[], maps path{}.
func documentKeys(t reflect.Type) []string {
	var keys []string
	walkKeys(t, "", &keys, map[reflect.Type]bool{})
	slices.Sort(keys)
	return keys
}

var (
	jsonMarshaler = reflect.TypeFor[json.Marshaler]()
	textMarshaler = reflect.TypeFor[encoding.TextMarshaler]()
)

// walkKeys records the keys of type t under path into keys. seen stops a
// type that holds itself.
func walkKeys(t reflect.Type, path string, keys *[]string, seen map[reflect.Type]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return
	}
	seen[t] = true
	defer delete(seen, t)
	for i := range t.NumField() {
		f := t.Field(i)
		tag, hasTag := f.Tag.Lookup("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" && opts == "" {
			continue
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		// An embedded struct without a name of its own has its fields
		// written at its level, as encoding/json does.
		if f.Anonymous && (!hasTag || name == "") && ft.Kind() == reflect.Struct {
			walkKeys(ft, path, keys, seen)
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		key := name
		if path != "" {
			key = path + "." + name
		}
		omit := ""
		if slices.Contains(strings.Split(opts, ","), "omitempty") || slices.Contains(strings.Split(opts, ","), "omitzero") {
			omit = " omitempty"
		}
		recordKey(f.Type, key, omit, keys, seen)
	}
}

// recordKey records key, of type t, and the keys inside it.
func recordKey(t reflect.Type, key, omit string, keys *[]string, seen map[reflect.Type]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	*keys = append(*keys, fmt.Sprintf("%s %s%s", key, jsonType(t), omit))
	if t.Implements(jsonMarshaler) || reflect.PointerTo(t).Implements(jsonMarshaler) ||
		t.Implements(textMarshaler) || reflect.PointerTo(t).Implements(textMarshaler) {
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		walkKeys(t, key, keys, seen)
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() != reflect.Uint8 {
			elem := t.Elem()
			for elem.Kind() == reflect.Pointer {
				elem = elem.Elem()
			}
			if elem.Kind() == reflect.Struct {
				walkKeys(elem, key+"[]", keys, seen)
			}
		}
	case reflect.Map:
		elem := t.Elem()
		for elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		if elem.Kind() == reflect.Struct {
			walkKeys(elem, key+"{}", keys, seen)
		}
	}
}

// jsonType names the JSON type encoding/json writes t as.
func jsonType(t reflect.Type) string {
	if t.Implements(jsonMarshaler) || reflect.PointerTo(t).Implements(jsonMarshaler) {
		return "json"
	}
	if t.Implements(textMarshaler) || reflect.PointerTo(t).Implements(textMarshaler) {
		return "string"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.String:
		return "string"
	case reflect.Struct:
		return "object"
	case reflect.Map:
		return "object of " + jsonType(deref(t.Elem()))
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return "string"
		}
		return "array of " + jsonType(deref(t.Elem()))
	case reflect.Interface:
		return "any"
	}
	return t.Kind().String()
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// TestDocumentKeysFollowEncodingJSON checks documentKeys against what
// encoding/json writes for a value with every field set, so that the
// golden files list the keys the documents really have.
func TestDocumentKeysFollowEncodingJSON(t *testing.T) {
	type inner struct {
		A string `json:"a"`
		B int    `json:"b,omitempty"`
	}
	type Embedded struct {
		E bool `json:"e"`
	}
	type doc struct {
		Version int `json:"schema_version"`
		Embedded
		Items  []inner           `json:"items"`
		ByName map[string]*inner `json:"by_name,omitempty"`
		Skip   string            `json:"-"`
		hidden string
		Any    any `json:"any,omitempty"`
	}
	want := []string{
		"any any omitempty",
		"by_name object of object omitempty",
		"by_name{}.a string",
		"by_name{}.b number omitempty",
		"e boolean",
		"items array of object",
		"items[].a string",
		"items[].b number omitempty",
		"schema_version number",
	}
	got := documentKeys(reflect.TypeOf(doc{hidden: ""}))
	if !slices.Equal(got, want) {
		t.Errorf("documentKeys:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	content, err := json.Marshal(doc{Version: 1, Embedded: Embedded{E: true}, Items: []inner{{A: "x", B: 1}},
		ByName: map[string]*inner{"n": {A: "y", B: 2}}, Any: "v"})
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(content, &top); err != nil {
		t.Fatal(err)
	}
	for _, k := range want {
		if strings.ContainsAny(strings.Fields(k)[0], ".[{") {
			continue
		}
		if _, ok := top[strings.Fields(k)[0]]; !ok {
			t.Errorf("encoding/json wrote no %q: %s", strings.Fields(k)[0], content)
		}
	}
	if len(top) != 5 {
		t.Errorf("encoding/json wrote %d top-level keys, want 5: %s", len(top), content)
	}
}
