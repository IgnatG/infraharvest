// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tftypes

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// IdentityID returns the ID infraharvest tells a resource imported by its
// resource identity by, rather than by an import ID: the identity's
// attributes as key=value, sorted by key and joined by commas, as
// Terraform writes an identity in its own log, such as
// account_id=123456789012,name=/app/logs,region=eu-west-2. The engine,
// the selection file and the reading of managed state all key such a
// resource by it, so each must make it the same way. Nil or empty
// identities have the ID "".
//
// Two identities never have the same ID: a backslash, and a comma in a
// key or a value, and an equals sign in a key, are written with a
// backslash before them (see ParseIdentityID, which reads it back). An
// identity with none of them has the ID above, as it always had.
func IdentityID(identity map[string]string) string {
	keys := make([]string, 0, len(identity))
	for k := range identity {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = keyEscaper.Replace(k) + "=" + valueEscaper.Replace(identity[k])
	}
	return strings.Join(parts, ",")
}

// keyEscaper and valueEscaper escape the text of an identity's key, and
// of a value, which may hold an equals sign as it is: the first one
// unescaped, in a pair, ends the key.
var (
	keyEscaper   = strings.NewReplacer(`\`, `\\`, `,`, `\,`, `=`, `\=`)
	valueEscaper = strings.NewReplacer(`\`, `\\`, `,`, `\,`)
)

// ParseIdentityID returns the identity an ID made by IdentityID is of.
// It returns false for text IdentityID doesn't make, such as a pair
// without an equals sign, a backslash before a character that needs none,
// or keys out of order or repeated.
func ParseIdentityID(id string) (map[string]string, bool) {
	identity := map[string]string{}
	if id == "" {
		return identity, true
	}
	var key, value strings.Builder
	inValue := false
	finish := func() bool {
		if !inValue {
			return false
		}
		identity[key.String()] = value.String()
		key.Reset()
		value.Reset()
		inValue = false
		return true
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		cur := &key
		if inValue {
			cur = &value
		}
		switch {
		case c == '\\':
			i++
			if i == len(id) || (id[i] != '\\' && id[i] != ',' && (id[i] != '=' || inValue)) {
				return nil, false
			}
			cur.WriteByte(id[i])
		case c == '=' && !inValue:
			inValue = true
		case c == ',':
			if !finish() {
				return nil, false
			}
		default:
			cur.WriteByte(c)
		}
	}
	if !finish() || IdentityID(identity) != id {
		return nil, false
	}
	return identity, true
}

// IdentityValues returns the attributes of an identity as Terraform's JSON
// output has them, in a resource's state or in the results of terraform
// query, as strings: a string as it is, a number in its shortest form
// (decoded as a float64, or as a json.Number, as terraform-json decodes
// the messages of terraform query) and a bool as true or false. Null attributes, which an identity leaves
// unset, are left out. It returns false for an identity with an
// attribute of another kind, such as a list, which IdentityID can't
// write.
func IdentityValues(identity map[string]any) (map[string]string, bool) {
	values := make(map[string]string, len(identity))
	for k, v := range identity {
		switch v := v.(type) {
		case nil:
		case string:
			values[k] = v
		case float64:
			values[k] = strconv.FormatFloat(v, 'f', -1, 64)
		case json.Number:
			f, err := v.Float64()
			if err != nil {
				return nil, false
			}
			values[k] = strconv.FormatFloat(f, 'f', -1, 64)
		case bool:
			values[k] = strconv.FormatBool(v)
		default:
			return nil, false
		}
	}
	return values, true
}
