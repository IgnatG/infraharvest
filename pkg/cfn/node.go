// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Node is a value in a template: one of Null, Bool, Number, String, List,
// Map and Func. JSON and YAML templates parse to the same nodes, so the
// converter never needs to know which format a template was written in.
type Node interface {
	node()
}

// Null is an explicit null, as YAML's ~ or JSON's null.
type Null struct{}

// Bool is a boolean literal.
type Bool bool

// Number is a numeric literal, kept as written so that converting it
// doesn't change its precision or form.
type Number string

// String is a string literal.
type String string

// List is a sequence of values.
type List []Node

// Map is a mapping of keys to values, in the order the template lists them.
// Keeping the order, rather than sorting, keeps the output in the order the
// author chose, and it is still the same for the same template.
type Map []Field

// Field is a key of a Map and its value.
type Field struct {
	Key   string
	Value Node
}

// Func is an intrinsic function or a condition function: a mapping with the
// single key Ref, Condition or Fn::<name>, or a YAML short-form tag such as
// !Sub. Name is the long form ("Ref", "Condition", "Fn::Sub") whichever form
// the template used, and Arg is the function's argument as written: a
// String for !Ref, a List for Fn::Join, and so on. YAML's !GetAtt Res.Attr
// is split into the list form, [Res, Attr], when it is parsed.
//
// A mapping with the single key Condition is only a function in the
// arguments of the condition functions (Fn::And, Fn::Or, Fn::Not) and as the
// YAML tag !Condition; elsewhere, such as in an IAM policy statement, it is
// an ordinary Map, so the parser leaves it one.
type Func struct {
	Name string
	Arg  Node
}

func (Null) node()   {}
func (Bool) node()   {}
func (Number) node() {}
func (String) node() {}
func (List) node()   {}
func (Map) node()    {}
func (Func) node()   {}

// Get returns the value of key, and whether the map has it.
func (m Map) Get(key string) (Node, bool) {
	for _, f := range m {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

// Keys returns the map's keys in order.
func (m Map) Keys() []string {
	keys := make([]string, len(m))
	for i, f := range m {
		keys[i] = f.Key
	}
	return keys
}

// set returns the map with key set to v: in place when the key is already
// there, so that a repeated key keeps its first position and its last value,
// as a JSON or YAML decoder would do.
func (m Map) set(key string, v Node) Map {
	for i := range m {
		if m[i].Key == key {
			m[i].Value = v
			return m
		}
	}
	return append(m, Field{Key: key, Value: v})
}

// MarshalJSON writes null, so that a Null in a comment reads as JSON does.
func (Null) MarshalJSON() ([]byte, error) {
	return []byte("null"), nil
}

// MarshalJSON writes the number as it was written when that is valid JSON,
// and as a string otherwise (YAML allows forms such as 0x1F that JSON
// doesn't).
func (n Number) MarshalJSON() ([]byte, error) {
	if numberPattern.MatchString(string(n)) {
		return []byte(n), nil
	}
	return json.Marshal(string(n))
}

// MarshalJSON writes the map's fields in order.
func (m Map) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range m {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := marshal(f.Key)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		value, err := marshal(f.Value)
		if err != nil {
			return nil, err
		}
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// MarshalJSON writes the function in its JSON long form, {"Fn::Sub": ...}.
func (f Func) MarshalJSON() ([]byte, error) {
	return Map{{Key: f.Name, Value: f.Arg}}.MarshalJSON()
}

// marshal encodes v as JSON without escaping <, > and &, which the
// converter's comments should show as written.
func marshal(v any) ([]byte, error) {
	if v == nil {
		return []byte("null"), nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// toJSON returns n as indented JSON, for the comments that keep what the
// converter couldn't convert.
func toJSON(n Node) string {
	compact, err := marshal(n)
	if err != nil {
		return "(unprintable value)"
	}
	var b bytes.Buffer
	if err := json.Indent(&b, compact, "", "  "); err != nil {
		return string(compact)
	}
	return b.String()
}

// compactJSON returns n as one line of JSON, for diagnostics.
func compactJSON(n Node) string {
	compact, err := marshal(n)
	if err != nil {
		return "(unprintable value)"
	}
	return string(compact)
}

// scalar returns the text of a String, Number or Bool, and whether n is one.
func scalar(n Node) (string, bool) {
	switch v := n.(type) {
	case String:
		return string(v), true
	case Number:
		return string(v), true
	case Bool:
		if v {
			return "true", true
		}
		return "false", true
	}
	return "", false
}

// isFunctionKey reports whether key, as the single key of a mapping, makes
// the mapping an intrinsic function.
func isFunctionKey(key string) bool {
	return key == "Ref" || strings.HasPrefix(key, "Fn::")
}

// mapOrFunc returns m as a Func when it is the long form of one.
func mapOrFunc(m Map) Node {
	if len(m) == 1 && isFunctionKey(m[0].Key) {
		return Func{Name: m[0].Key, Arg: m[0].Value}
	}
	return m
}
