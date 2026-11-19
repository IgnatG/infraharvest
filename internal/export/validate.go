// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package export

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/IgnatG/infraharvest/schemas"
)

// schemaFile is the file of a document's schema, in the schemas package.
func schemaFile(document string) string {
	return document + "-v1.schema.json"
}

// Schema returns the JSON Schema (2020-12) of a document, as published.
func Schema(document string) ([]byte, error) {
	// The schemas package has others, such as drift.json's, which are no
	// documents of export.
	if !slices.Contains(Documents, document) {
		return nil, fmt.Errorf("unknown document %q", document)
	}
	content, err := schemas.FS.ReadFile(schemaFile(document))
	if err != nil {
		return nil, fmt.Errorf("unknown document %q", document)
	}
	return content, nil
}

// checker validates a document against its schema. The arrays of items
// that the document's top level lists, such as the inventory's resources,
// can hold a hundred thousand entries, and every entry is checked against
// the same item schema: those are checked on their own, in parallel, and
// the rest of the document with the array empty. That is the same check
// as the whole document's only where the array's schema says nothing but
// its type and the schema of its items, and the top level says nothing
// that depends on the array's content, which newChecker makes sure of:
// otherwise the document is checked whole.
type checker struct {
	whole *jsonschema.Resolved
	// items are the schemas of the items of the arrays checked on their
	// own, by property.
	items map[string]*jsonschema.Resolved
}

var (
	checkersMu sync.Mutex
	checkers   = map[string]*checker{}
)

// checkerOf returns the document's checker.
func checkerOf(document string) (*checker, error) {
	checkersMu.Lock()
	defer checkersMu.Unlock()
	if c, ok := checkers[document]; ok {
		return c, nil
	}
	content, err := Schema(document)
	if err != nil {
		return nil, err
	}
	c, err := newChecker(content)
	if err != nil {
		return nil, fmt.Errorf("%s schema: %w", document, err)
	}
	checkers[document] = c
	return c, nil
}

// topLevelKeys are the keywords a document's schema may have at its top
// level for its arrays to be checked on their own: none of them depends on
// what an array holds.
var topLevelKeys = map[string]bool{
	"$schema": true, "$id": true, "title": true, "description": true, "type": true,
	"additionalProperties": true, "required": true, "properties": true, "$defs": true,
}

func newChecker(content []byte) (*checker, error) {
	whole, err := resolveSchema(content)
	if err != nil {
		return nil, err
	}
	c := &checker{whole: whole, items: map[string]*jsonschema.Resolved{}}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(content, &top); err != nil {
		return nil, err
	}
	for key := range top {
		if !topLevelKeys[key] {
			return c, nil
		}
	}
	var properties map[string]map[string]json.RawMessage
	if err := json.Unmarshal(top["properties"], &properties); err != nil {
		return c, nil //nolint:nilerr // properties of another form: checked whole
	}
	for name, p := range properties {
		var typ string
		var items map[string]json.RawMessage
		if len(p) != 2 || json.Unmarshal(p["type"], &typ) != nil || typ != "array" || json.Unmarshal(p["items"], &items) != nil {
			continue
		}
		// The items' schema, as one with the document's definitions, which
		// its references name.
		item := map[string]json.RawMessage{}
		maps.Copy(item, items)
		for _, key := range []string{"$schema", "$id", "$defs"} {
			if raw, ok := top[key]; ok {
				if _, clash := item[key]; clash {
					item = nil
					break
				}
				item[key] = raw
			}
		}
		if item == nil {
			continue
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		if c.items[name], err = resolveSchema(encoded); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func resolveSchema(content []byte) (*jsonschema.Resolved, error) {
	var s jsonschema.Schema
	if err := json.Unmarshal(content, &s); err != nil {
		return nil, err
	}
	return s.Resolve(nil)
}

// array is an array of a document checked an item at a time: n items, of
// which item returns the i-th as instance would.
type array struct {
	n    int
	item func(i int) (any, error)
}

// check validates rest, a document as encoding/json decodes it into an any
// (see instance), with each of arrays, by property, as the value of that
// property in place of the empty array rest has.
func (c *checker) check(rest any, arrays map[string]array) error {
	if err := c.whole.Validate(rest); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(arrays)) {
		if err := checkItems(c.items[name], name, arrays[name]); err != nil {
			return err
		}
	}
	return nil
}

// checkItems validates the items of a against item, in parallel. Each
// worker stops at the first item it finds failing, and the errors of all
// that did are returned.
func checkItems(item *jsonschema.Resolved, name string, a array) error {
	workers := min(runtime.GOMAXPROCS(0), max(1, a.n/1000))
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := w; i < a.n; i += workers {
				instance, err := a.item(i)
				if err == nil {
					err = item.Validate(instance)
				}
				if err != nil {
					errs[w] = fmt.Errorf("%s/%d: %w", name, i, err)
					return
				}
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// validateDocument checks doc, a document as Build builds it, against the
// schema of document, as validate checks its encoding. It builds what the
// validator takes of each item of the arrays checked on their own (see
// checker) in the worker that checks it.
func validateDocument(document string, doc any) error {
	c, err := checkerOf(document)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for name := range c.items {
		names[name] = true
	}
	rest, split, err := splitInstance(doc, names)
	if err != nil {
		return fmt.Errorf("the %s document has %w, which the check doesn't know", document, err)
	}
	arrays := map[string]array{}
	for name, v := range split {
		arrays[name] = array{n: v.Len(), item: func(i int) (any, error) { return instance(v.Index(i)) }}
	}
	return c.check(rest, arrays)
}
