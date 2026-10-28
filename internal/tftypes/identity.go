// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tftypes

import (
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
func IdentityID(identity map[string]string) string {
	keys := make([]string, 0, len(identity))
	for k := range identity {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + identity[k]
	}
	return strings.Join(parts, ",")
}

// IdentityValues returns the attributes of an identity as Terraform's JSON
// output has them, in a resource's state or in the results of terraform
// query, as strings: a string as it is, a number in its shortest form
// and a bool as true or false. Null attributes, which an identity leaves
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
		case bool:
			values[k] = strconv.FormatBool(v)
		default:
			return nil, false
		}
	}
	return values, true
}
