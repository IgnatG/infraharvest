// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package driftpolicy decides what infraharvest drift does with each
// change it finds in the cloud: accept it into the code, or keep the
// code's value, which the next apply puts back. It holds the rules of
// --accept, --keep and --rules, the decisions file people review and
// edit, the order they decide in (Decider), and the preview the MCP
// server shows before a run with them writes anything. The importer
// applies what it decides; the MCP server reads it without the importer.
package driftpolicy

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/IgnatG/infraharvest/internal/engine"
	"github.com/IgnatG/infraharvest/internal/report"
)

// Rule decides, for the changes it matches, whether to accept the
// cloud's value into the code or to keep the code's, which the next apply
// puts back.
type Rule struct {
	Accept bool
	// Field is what Pattern matches: a change's attribute, type, address
	// or root; "" matches every change, as all does.
	Field string
	// Pattern is a pattern in which * matches any text, such as tags.*.
	Pattern string
}

// ruleFields are the fields a Rule matches.
var ruleFields = []string{"attribute", "type", "address", "root"}

// ParseRule parses a rule as --accept (accept) and --keep take it: all,
// or <field>=<pattern> for the field attribute, type, address or root,
// such as attribute=tags.* or type=aws_security_group.
func ParseRule(s string, accept bool) (Rule, error) {
	if s == "all" {
		return Rule{Accept: accept}, nil
	}
	field, pattern, ok := strings.Cut(s, "=")
	if !ok || pattern == "" || !slices.Contains(ruleFields, field) {
		flag := "--keep"
		if accept {
			flag = "--accept"
		}
		return Rule{}, fmt.Errorf("%s %q: want all, or attribute=, type=, address= or root= with a pattern, such as attribute=tags.*", flag, s)
	}
	return Rule{Accept: accept, Field: field, Pattern: pattern}, nil
}

// ParseRuleLine parses a rule as an ordered list of accept and keep
// rules takes it, as a line of drift's --rules file, which the GitHub
// Action's drift-accept becomes, and an entry of the MCP server's
// drift_accept rules do: accept or keep, then the rule as ParseRule
// takes it, such as keep attribute=tags.*; a rule alone is accepted.
func ParseRuleLine(s string) (Rule, error) {
	s = strings.TrimSpace(s)
	accept, rule := true, s
	if i := strings.IndexFunc(s, unicode.IsSpace); i >= 0 {
		switch s[:i] {
		case "accept":
			rule = strings.TrimSpace(s[i:])
		case "keep":
			accept, rule = false, strings.TrimSpace(s[i:])
		}
	}
	r, err := ParseRule(rule, accept)
	if err != nil {
		return Rule{}, fmt.Errorf("rule %q: want accept or keep, then all, or attribute=, type=, address= or root= with a pattern, such as keep attribute=tags.*", s)
	}
	return r, nil
}

// ParseRuleLines parses lines, each a rule as ParseRuleLine takes it, in
// order, as drift's --rules file and the MCP server's drift_accept rules
// have them: blank lines and lines starting with # are skipped. An error
// says which line, counting from 1.
func ParseRuleLines(lines []string) ([]Rule, error) {
	var rules []Rule
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule, err := ParseRuleLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// String is the rule as --accept and --keep take it.
func (r Rule) String() string {
	if r.Field == "" {
		return "all"
	}
	return r.Field + "=" + r.Pattern
}

// matches reports whether the rule matches c. A change of tags several
// resources get from one place, such as local.tags (see
// report.DriftChange.Covers), matches a type rule if one of those
// resources is of the type, and an address rule if the place's address
// or one of theirs matches.
func (r Rule) matches(c report.DriftChange) bool {
	matches := func(value string) bool { return matchPattern(r.Pattern, value) }
	var value string
	switch r.Field {
	case "":
		return true
	case "attribute":
		value = c.Attribute
	case "type":
		if len(c.Covers) > 0 {
			return slices.ContainsFunc(c.Covers, func(a string) bool { return matches(engine.AddressType(a)) })
		}
		value = c.Type
	case "address":
		if slices.ContainsFunc(c.Covers, matches) {
			return true
		}
		value = c.Address
	case "root":
		value = c.Root
	default:
		return false
	}
	return matches(value)
}

// matchPattern reports whether value matches pattern, in which * matches
// any text, as the selection file's patterns do. Rules are few and drift
// matches each change against them once, so a pattern is compiled where
// it is matched rather than kept.
func matchPattern(pattern, value string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}
	parts := strings.Split(pattern, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$").MatchString(value)
}
