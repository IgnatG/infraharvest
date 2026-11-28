// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfstate

import (
	"encoding/json"
	"slices"
	"strings"
)

// TrustStatement is one statement of the trust policy of an aws_iam_role (its
// assume_role_policy), reduced to what says who may assume the role: the
// policy's own text is not kept, and neither are its Sid, its resources or
// the keys and values of its conditions.
type TrustStatement struct {
	// Effect is "Allow" or "Deny", as the policy writes it.
	Effect string
	// Assumes is true when the statement's actions include
	// sts:AssumeRole, by name or by a wildcard (sts:Assume*, sts:*, *),
	// or, for a NotAction, when it doesn't exclude it: with NotAction
	// ["sts:AssumeRole"] it is false. For an Effect of "Deny" it says the
	// action is denied. Names are matched without regard to case, as IAM
	// does.
	Assumes bool
	// AssumesWithWebIdentity is Assumes for sts:AssumeRoleWithWebIdentity,
	// the action a Federated principal such as an OIDC provider uses.
	AssumesWithWebIdentity bool
	// AssumesWithSAML is Assumes for sts:AssumeRoleWithSAML, the action a
	// Federated SAML provider uses.
	AssumesWithSAML bool
	// NotAction is true for a statement that has a NotAction: it applies
	// to every action but those it names, and the three Assumes fields say
	// which of the assume actions that leaves.
	NotAction bool
	// Principals are the principals the statement names, or, with
	// NotPrincipal, the ones it excludes. Empty where it names none. The
	// wildcard principal, whether the policy writes "*" or {"AWS": "*"},
	// is the one principal {Type: "*", Value: "*"}.
	Principals []TrustPrincipal
	// NotPrincipal is true for a statement that has a NotPrincipal: it
	// applies to everyone but its Principals.
	NotPrincipal bool
	// Conditional is true when the statement has a Condition block, or a
	// NotAction (see NotAction). The condition is not evaluated and not
	// kept.
	Conditional bool
}

// TrustPrincipal is one principal of a trust statement. Type is AWS, Service,
// Federated or CanonicalUser, and "*" for the wildcard principal, which every
// policy that names everyone as "*" or {"AWS": "*"} has as {"*", "*"} (a
// "*" under another type, such as {"Service": "*"}, keeps its type). Value
// is as the policy writes it: an account ID, an ARN, a service name.
type TrustPrincipal struct {
	// Type is the kind of principal, or "*" for the wildcard principal.
	Type string
	// Value is the principal, as the policy writes it.
	Value string
}

// Caps on what is read from one trust policy. Real policies are a few KB (IAM
// allows 2048 characters by default, 4096 on request), so a policy over a cap
// is read as unreadable and nothing of it is kept.
const (
	maxTrustText       = 64 << 10
	maxTrustStatements = 100
	maxTrustPrincipals = 500
)

// trustStatements reads the trust policy in document. It returns false where
// the document is not a policy this reader understands, or is over a cap.
func trustStatements(document string) ([]TrustStatement, bool) {
	if len(document) > maxTrustText {
		return nil, false
	}
	var p struct {
		Statement json.RawMessage `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(document), &p); err != nil || len(p.Statement) == 0 {
		return nil, false
	}
	raw := []json.RawMessage{p.Statement}
	if strings.HasPrefix(strings.TrimSpace(string(p.Statement)), "[") {
		if err := json.Unmarshal(p.Statement, &raw); err != nil {
			return nil, false
		}
	}
	if len(raw) > maxTrustStatements {
		return nil, false
	}
	var out []TrustStatement
	for _, r := range raw {
		var s struct {
			Effect       string          `json:"Effect"`
			Action       json.RawMessage `json:"Action"`
			NotAction    json.RawMessage `json:"NotAction"`
			Principal    json.RawMessage `json:"Principal"`
			NotPrincipal json.RawMessage `json:"NotPrincipal"`
			Condition    json.RawMessage `json:"Condition"`
		}
		if err := json.Unmarshal(r, &s); err != nil {
			return nil, false
		}
		st := TrustStatement{Effect: s.Effect}
		if len(s.NotAction) > 0 && string(s.NotAction) != "null" {
			excluded, ok := stringList(s.NotAction)
			if !ok {
				return nil, false
			}
			st.NotAction, st.Conditional = true, true
			st.Assumes = !matchesAny(excluded, assumeRole)
			st.AssumesWithWebIdentity = !matchesAny(excluded, assumeRoleWithWebIdentity)
			st.AssumesWithSAML = !matchesAny(excluded, assumeRoleWithSAML)
		} else {
			actions, ok := stringList(s.Action)
			if !ok {
				return nil, false
			}
			st.Assumes = matchesAny(actions, assumeRole)
			st.AssumesWithWebIdentity = matchesAny(actions, assumeRoleWithWebIdentity)
			st.AssumesWithSAML = matchesAny(actions, assumeRoleWithSAML)
		}
		pr := s.Principal
		if len(s.NotPrincipal) > 0 && string(s.NotPrincipal) != "null" {
			pr, st.NotPrincipal = s.NotPrincipal, true
		}
		var ok bool
		if st.Principals, ok = trustPrincipals(pr); !ok {
			return nil, false
		}
		if c := strings.TrimSpace(string(s.Condition)); c != "" && c != "null" && c != "{}" {
			st.Conditional = true
		}
		out = append(out, st)
	}
	return out, true
}

// The actions that assume a role.
const (
	assumeRole                = "sts:assumerole"
	assumeRoleWithWebIdentity = "sts:assumerolewithwebidentity"
	assumeRoleWithSAML        = "sts:assumerolewithsaml"
)

// matchesAny reports whether any of the patterns, IAM action names that may
// have the wildcards * and ?, matches action, a name in lower case. IAM
// action names are not case sensitive.
func matchesAny(patterns []string, action string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return wildcardMatch(strings.ToLower(p), action) })
}

// wildcardMatch reports whether name matches pattern, where * stands for any
// run of characters, none included, and ? for any one.
func wildcardMatch(pattern, name string) bool {
	p, n := 0, 0
	star, mark := -1, 0
	for n < len(name) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == name[n]):
			p++
			n++
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, n
			p++
		case star >= 0:
			p = star + 1
			mark++
			n = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// trustPrincipals reads a Principal: "*", or an object of string or list
// values by type. It returns false for a principal that is not one of those,
// that has a value of another type, or that names more than maxTrustPrincipals.
func trustPrincipals(raw json.RawMessage) ([]TrustPrincipal, bool) {
	if absent(raw) {
		return nil, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []TrustPrincipal{{Type: "*", Value: s}}, true
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil, false
	}
	var out []TrustPrincipal
	for _, t := range []string{"AWS", "Service", "Federated", "CanonicalUser"} {
		vs, ok := stringList(m[t])
		if !ok || len(out)+len(vs) > maxTrustPrincipals {
			return nil, false
		}
		for _, v := range vs {
			p := TrustPrincipal{Type: t, Value: v}
			if t == "AWS" && v == "*" {
				// {"AWS": "*"} is the same as "*".
				p.Type = "*"
			}
			if p.Type == "*" && slices.Contains(out, p) {
				continue
			}
			out = append(out, p)
		}
	}
	return out, true
}

// absent is true for a missing or null value.
func absent(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// stringList reads a string or a list of strings. A missing or null value is
// an empty list; the second result is false for any other value, such as a
// list with a non-string in it.
func stringList(raw json.RawMessage) ([]string, bool) {
	if absent(raw) {
		return nil, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil, true
		}
		return []string{s}, true
	}
	var l []string
	if json.Unmarshal(raw, &l) == nil {
		return l, true
	}
	return nil, false
}

// roleTrust reads the assume_role_policy attribute of a role: a JSON string.
func roleTrust(raw json.RawMessage) (statements []TrustStatement, unreadable bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	// A string of maxTrustText characters is at most six times that escaped.
	if len(raw) > 6*maxTrustText+2 {
		return nil, true
	}
	var doc string
	if json.Unmarshal(raw, &doc) != nil || strings.TrimSpace(doc) == "" {
		return nil, true
	}
	st, ok := trustStatements(doc)
	return st, !ok
}
