// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"fmt"
	"slices"
	"strings"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// DriftRule decides, for the changes it matches, whether to accept the
// cloud's value into the code or to keep the code's, which the next apply
// puts back.
type DriftRule struct {
	Accept bool
	// Field is what Pattern matches: a change's attribute, type, address
	// or root; "" matches every change, as all does.
	Field string
	// Pattern is a pattern in which * matches any text, such as tags.*.
	Pattern string
}

// driftRuleFields are the fields a DriftRule matches.
var driftRuleFields = []string{"attribute", "type", "address", "root"}

// ParseDriftRule parses a rule as --accept (accept) and --keep take it:
// all, or <field>=<pattern> for the field attribute, type, address or
// root, such as attribute=tags.* or type=aws_security_group.
func ParseDriftRule(s string, accept bool) (DriftRule, error) {
	if s == "all" {
		return DriftRule{Accept: accept}, nil
	}
	field, pattern, ok := strings.Cut(s, "=")
	if !ok || pattern == "" || !slices.Contains(driftRuleFields, field) {
		flag := "--keep"
		if accept {
			flag = "--accept"
		}
		return DriftRule{}, fmt.Errorf("%s %q: want all, or attribute=, type=, address= or root= with a pattern, such as attribute=tags.*", flag, s)
	}
	return DriftRule{Accept: accept, Field: field, Pattern: pattern}, nil
}

// String is the rule as --accept and --keep take it.
func (r DriftRule) String() string {
	if r.Field == "" {
		return "all"
	}
	return r.Field + "=" + r.Pattern
}

// matches reports whether the rule matches c, a change of the root at
// root in the output directory. A change of tags several resources get
// from one place, such as local.tags (see engine.DriftChange.Covers),
// matches a type rule if one of those resources is of the type, and an
// address rule if the place's address or one of theirs matches.
func (r DriftRule) matches(root string, c engine.DriftChange) bool {
	patterns := selection.Patterns{r.Pattern}
	var value string
	switch r.Field {
	case "":
		return true
	case "attribute":
		value = c.Attribute
	case "type":
		if len(c.Covers) > 0 {
			return slices.ContainsFunc(c.Covers, func(a string) bool { return patterns.Matches(addressType(a)) })
		}
		value = c.Type
	case "address":
		if slices.ContainsFunc(c.Covers, patterns.Matches) {
			return true
		}
		value = c.Address
	case "root":
		value = root
	default:
		return false
	}
	return patterns.Matches(value)
}

// addressType returns the type of the resource at address, such as
// aws_s3_bucket for module.logs.aws_s3_bucket.this[0], or for
// module.logs["a.b"].aws_s3_bucket.this.
func addressType(address string) string {
	parts := addressNames(address)
	for len(parts) > 2 && parts[0] == "module" {
		parts = parts[2:]
	}
	if len(parts) > 1 && parts[0] == "data" {
		parts = parts[1:]
	}
	return parts[0]
}

// acceptsDrift reports whether rules accept c, a change of the root at
// root: the last rule that matches it decides, and none keeps it.
func acceptsDrift(rules []DriftRule, root string, c engine.DriftChange) bool {
	accept := false
	for _, r := range rules {
		if r.matches(root, c) {
			accept = r.Accept
		}
	}
	return accept
}

// addressNames returns the names of an address, split at its dots,
// without its instance keys, whose quoted strings may have dots of their
// own: module, logs, aws_s3_bucket and this for
// module.logs["a.b"].aws_s3_bucket.this[0].
func addressNames(address string) []string {
	var names []string
	var name strings.Builder
	depth, quoted, escaped := 0, false, false
	for _, r := range address {
		switch {
		case quoted:
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == '"':
				quoted = false
			}
		case r == '"' && depth > 0:
			quoted = true
		case r == '[':
			depth++
		case r == ']' && depth > 0:
			depth--
		case depth > 0:
		case r == '.':
			names = append(names, name.String())
			name.Reset()
		default:
			name.WriteRune(r)
		}
	}
	return append(names, name.String())
}
