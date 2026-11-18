// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"encoding/json"
	"maps"
	"reflect"
	"testing"
	"time"

	"github.com/IgnatG/infraharvest/internal/report"
)

// fill sets every field v reaches to a value that isn't empty, so that a
// field instance doesn't follow shows.
func fill(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fill(v.Elem())
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i))
			}
		}
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int:
		v.SetInt(3)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := range 2 {
			fill(s.Index(i))
		}
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		e := reflect.New(v.Type().Elem()).Elem()
		fill(e)
		m.SetMapIndex(reflect.ValueOf("k"), e)
		v.Set(m)
	}
}

// roundTrip is what encoding/json makes of v's encoding, decoded into an
// any.
func roundTrip(t *testing.T, v any) any {
	t.Helper()
	content, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var back any
	if err := json.Unmarshal(content, &back); err != nil {
		t.Fatal(err)
	}
	return back
}

// TestInstanceIsTheEncoding checks that instance, which the check of a
// document validates, is what validating the document's encoding would
// read: for every document type, empty, with every field set, and as Build
// builds them.
func TestInstanceIsTheEncoding(t *testing.T) {
	t.Parallel()
	docs := []any{&inventoryDoc{}, &runDoc{}, &gateDoc{}}
	for _, empty := range []any{&inventoryDoc{}, &runDoc{}, &gateDoc{}} {
		full := reflect.New(reflect.TypeOf(empty).Elem())
		fill(full)
		docs = append(docs, full.Interface())
	}
	out := writeOut(t)
	r, err := report.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := buildInventory(r, Options{Out: out}, "00000000-0000-5000-8000-000000000000", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	docs = append(docs, inv, buildRun(r, "id", time.Time{}, time.Time{}), buildGate(r, "id"))
	for _, doc := range docs {
		got, err := instance(reflect.ValueOf(doc))
		if err != nil {
			t.Fatalf("%T: %v", doc, err)
		}
		if want := roundTrip(t, doc); !reflect.DeepEqual(got, want) {
			t.Errorf("%T:\n got %#v\nwant %#v", doc, got, want)
		}
	}
}

// TestInstanceRefusesWhatItDoesNotKnow checks that a kind encoding/json
// writes differently from what instance knows fails the check rather
// than passing unchecked.
func TestInstanceRefusesWhatItDoesNotKnow(t *testing.T) {
	t.Parallel()
	for _, v := range []any{
		struct{ F float64 }{1.5},
		struct {
			F int `json:"f,string"`
		}{1},
		map[int]string{1: "a"},
		struct{ component }{},
	} {
		if _, err := instance(reflect.ValueOf(v)); err == nil {
			t.Errorf("%T passed", v)
		}
	}
}

// TestCheckIsTheWholeSchemas checks that the check of a document, which
// checks its resources one at a time, rejects what checking the whole
// document against its schema rejects, and passes what it passes.
func TestCheckIsTheWholeSchemas(t *testing.T) {
	t.Parallel()
	c, err := checkerOf(DocInventory)
	if err != nil {
		t.Fatal(err)
	}
	if c.items["resources"] == nil {
		t.Fatalf("the inventory's resources aren't checked on their own: %v", c.items)
	}
	out := writeOut(t)
	good := build(t, Options{Out: out, Document: DocInventory})
	mutations := map[string]func(doc map[string]any, res map[string]any){
		"none":                   func(map[string]any, map[string]any) {},
		"empty id":               func(_, res map[string]any) { res["id"] = "" },
		"empty account":          func(_, res map[string]any) { res["account"] = "" },
		"no type":                func(_, res map[string]any) { delete(res, "type") },
		"other status":           func(_, res map[string]any) { res["status"] = "mine" },
		"other owner":            func(_, res map[string]any) { res["owner"] = "me" },
		"empty region":           func(_, res map[string]any) { res["region"] = "" },
		"tag not a string":       func(_, res map[string]any) { res["tags"] = map[string]any{"a": 1.0} },
		"extra field":            func(_, res map[string]any) { res["attributes"] = map[string]any{} },
		"resource not an object": func(doc, _ map[string]any) { doc["resources"].([]any)[0] = "x" },
		"resources null":         func(doc, _ map[string]any) { doc["resources"] = nil },
		"resources an object":    func(doc, _ map[string]any) { doc["resources"] = map[string]any{} },
		"no resources":           func(doc, _ map[string]any) { delete(doc, "resources") },
		"empty scope":            func(doc, _ map[string]any) { doc["scopes"] = []any{""} },
		"scopes null":            func(doc, _ map[string]any) { doc["scopes"] = nil },
		"extra top-level field":  func(doc, _ map[string]any) { doc["x"] = 1.0 },
	}
	for name, mutate := range mutations {
		var doc map[string]any
		if err := json.Unmarshal(good, &doc); err != nil {
			t.Fatal(err)
		}
		mutate(doc, doc["resources"].([]any)[0].(map[string]any))
		content, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var instance any
		if err := json.Unmarshal(content, &instance); err != nil {
			t.Fatal(err)
		}
		whole := c.whole.Validate(instance)
		split := validate(DocInventory, content)
		if (whole == nil) != (split == nil) {
			t.Errorf("%s: the whole schema says %v, the check %v", name, whole, split)
		}
		if name != "none" && split == nil {
			t.Errorf("%s: passed", name)
		}
	}
}

// TestValidateDocumentRejects checks that a document Build would write
// fails the check when it breaks its schema, as its encoding would.
func TestValidateDocumentRejects(t *testing.T) {
	t.Parallel()
	out := writeOut(t)
	r, err := report.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := buildInventory(r, Options{Out: out}, "00000000-0000-5000-8000-000000000000", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDocument(DocInventory, inv); err != nil {
		t.Fatal(err)
	}
	inv.Resources[len(inv.Resources)-1].Account = ""
	if err := validateDocument(DocInventory, inv); err == nil {
		t.Error("a resource without an account passed")
	}
	inv.Resources = nil
	if err := validateDocument(DocInventory, inv); err == nil {
		t.Error("resources that encode as null passed")
	}
}

// validate checks content, a JSON document, against the schema of
// document. Schemas say additionalProperties false, so a field the schema
// doesn't name fails it.
func validate(document string, content []byte) error {
	var doc any
	if err := json.Unmarshal(content, &doc); err != nil {
		return err
	}
	c, err := checkerOf(document)
	if err != nil {
		return err
	}
	top, ok := doc.(map[string]any)
	if !ok {
		return c.whole.Validate(doc)
	}
	rest := maps.Clone(top)
	arrays := map[string]array{}
	for name := range c.items {
		if items, ok := top[name].([]any); ok {
			rest[name] = []any{}
			arrays[name] = array{n: len(items), item: func(i int) (any, error) { return items[i], nil }}
		}
	}
	return c.check(rest, arrays)
}
