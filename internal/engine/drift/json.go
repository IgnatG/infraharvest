// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package drift

import (
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// equivalentJSON reports whether before and after are both strings that
// hold the same JSON document, an object or an array, however it is
// formatted: key order, whitespace and the spelling of numbers aside. An
// IAM policy (an object with a Statement) is compared as AWS reads it, and
// as the AWS provider compares policies: its statements in any order, a
// single value the same as a list of one, the values of Action, Resource,
// Principal and Condition in any order, and an empty Sid the same as none.
// Policies a cloud gives back reformatted are then not drift.
func equivalentJSON(before, after any) bool {
	b, ok := before.(string)
	if !ok {
		return false
	}
	a, ok := after.(string)
	if !ok {
		return false
	}
	bv, ok := decodeJSONDocument(b)
	if !ok {
		return false
	}
	av, ok := decodeJSONDocument(a)
	if !ok {
		return false
	}
	if isPolicy(bv) || isPolicy(av) {
		bv, av = normalizePolicy(bv), normalizePolicy(av)
	}
	return reflect.DeepEqual(bv, av)
}

// decodeJSONDocument decodes s when it is a JSON object or array, and
// nothing else: a string such as "1" or "true" stays a string.
func decodeJSONDocument(s string) (any, bool) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return nil, false
	}
	return v, true
}

// isPolicy reports whether v is an IAM policy document: an object with a
// Statement.
func isPolicy(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	_, ok = m["Statement"]
	return ok
}

// normalizePolicy returns the policy v with its statements normalized
// (see normalizeStatement) and sorted, a single statement as a list of
// one.
func normalizePolicy(v any) any {
	doc, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(doc))
	for k, val := range doc {
		out[k] = val
	}
	var statements []any
	switch s := doc["Statement"].(type) {
	case []any:
		statements = s
	case map[string]any:
		statements = []any{s}
	default:
		return out
	}
	normalized := make([]any, 0, len(statements))
	for _, s := range statements {
		if m, ok := s.(map[string]any); ok {
			normalized = append(normalized, normalizeStatement(m))
		} else {
			normalized = append(normalized, s)
		}
	}
	sortByJSON(normalized)
	out["Statement"] = normalized
	return out
}

// normalizeStatement returns the statement st with the values of Action,
// NotAction, Resource and NotResource, of each principal type and of each
// condition key as sorted sets (see stringSet), the principal "*" as
// {"AWS": ["*"]}, and without an empty Sid.
func normalizeStatement(st map[string]any) map[string]any {
	out := make(map[string]any, len(st))
	for k, v := range st {
		switch k {
		case "Sid":
			if s, ok := v.(string); ok && s == "" {
				continue
			}
			out[k] = v
		case "Action", "NotAction", "Resource", "NotResource":
			out[k] = stringSet(v)
		case "Principal", "NotPrincipal":
			if s, ok := v.(string); ok && s == "*" {
				v = map[string]any{"AWS": s}
			}
			out[k] = mapOf(v, stringSet)
		case "Condition":
			out[k] = mapOf(v, func(keys any) any { return mapOf(keys, stringSet) })
		default:
			out[k] = v
		}
	}
	return out
}

// mapOf returns the object v with f applied to each of its values, or v
// as it is when it isn't an object.
func mapOf(v any, f func(any) any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		out[k] = f(val)
	}
	return out
}

// stringSet returns v, a value or a list of values, as a sorted list
// without duplicates, booleans and numbers as the strings IAM compares
// them as. Anything else, such as an object, is returned as it is.
func stringSet(v any) any {
	items, ok := v.([]any)
	if !ok {
		items = []any{v}
	}
	set := make([]string, 0, len(items))
	for _, item := range items {
		switch s := item.(type) {
		case string:
			set = append(set, s)
		case bool:
			set = append(set, strconv.FormatBool(s))
		case float64:
			set = append(set, strconv.FormatFloat(s, 'f', -1, 64))
		default:
			return v
		}
	}
	slices.Sort(set)
	set = slices.Compact(set)
	out := make([]any, len(set))
	for i, s := range set {
		out[i] = s
	}
	return out
}

// sortByJSON sorts values by their JSON encoding, which writes object
// keys in order: the same values sort the same, whatever order they came
// in.
func sortByJSON(values []any) {
	type entry struct {
		key   string
		value any
	}
	entries := make([]entry, len(values))
	for i, v := range values {
		b, err := json.Marshal(v)
		if err != nil {
			// Decoded JSON always encodes again: leave the order as it is.
			return
		}
		entries[i] = entry{string(b), v}
	}
	slices.SortStableFunc(entries, func(a, b entry) int { return strings.Compare(a.key, b.key) })
	for i, e := range entries {
		values[i] = e.value
	}
}

// driftSameValue reports whether before and after hold the same value: equal,
// or maps and lists whose values are the same value in turn, with JSON
// documents compared by equivalentJSON. A map whose only difference is a
// reformatted policy is then the same map, as its attribute is.
func driftSameValue(before, after any) bool {
	if reflect.DeepEqual(before, after) {
		return true
	}
	switch b := before.(type) {
	case map[string]any:
		a, ok := after.(map[string]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for k, v := range b {
			w, ok := a[k]
			if !ok || !driftSameValue(v, w) {
				return false
			}
		}
		return true
	case []any:
		a, ok := after.([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range b {
			if !driftSameValue(b[i], a[i]) {
				return false
			}
		}
		return true
	}
	return equivalentJSON(before, after)
}
