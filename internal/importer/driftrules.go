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
// root in the output directory.
func (r DriftRule) matches(root string, c engine.DriftChange) bool {
	var value string
	switch r.Field {
	case "":
		return true
	case "attribute":
		value = c.Attribute
	case "type":
		value = c.Type
	case "address":
		value = c.Address
	case "root":
		value = root
	default:
		return false
	}
	return selection.Patterns{r.Pattern}.Matches(value)
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
