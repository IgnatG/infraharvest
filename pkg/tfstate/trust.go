// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfstate

import (
	"encoding/json"
	"path"
	"strings"
)

// TrustStatement is one statement of the trust policy of an aws_iam_role (its
// assume_role_policy), reduced to what says who may assume the role: the
// policy's own text is not kept, and neither are its Sid, its resources or
// the keys and values of its conditions.
type TrustStatement struct {
	// Effect is "Allow" or "Deny", as the policy writes it.
	Effect string
	// Assumes is true when the statement's actions include sts:AssumeRole,
	// by name or by a wildcard (sts:Assume*, sts:*, *), or are given as a
	// NotAction, which is read as including it. A statement of only
	// sts:AssumeRoleWithWebIdentity or sts:AssumeRoleWithSAML does not.
	Assumes bool
	// Principals are the principals the statement names, or, with
	// NotPrincipal, the ones it excludes. Empty where it names none.
	Principals []TrustPrincipal
	// NotPrincipal is true for a statement that has a NotPrincipal: it
	// applies to everyone but its Principals.
	NotPrincipal bool
	// Conditional is true when the statement has a Condition block, or a
	// NotAction. The condition is not evaluated and not kept.
	Conditional bool
}

// TrustPrincipal is one principal of a trust statement. Type is AWS, Service,
// Federated or CanonicalUser, and "*" for the bare wildcard principal "*"
// (whose Value is "*" too). Value is as the policy writes it: an account ID,
// an ARN, a service name.
type TrustPrincipal struct {
	Type, Value string
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
		switch {
		case len(s.NotAction) > 0 && string(s.NotAction) != "null":
			st.Assumes, st.Conditional = true, true
		default:
			actions, ok := stringList(s.Action)
			if !ok {
				return nil, false
			}
			for _, a := range actions {
				if ok, _ := path.Match(strings.ToLower(a), "sts:assumerole"); ok {
					st.Assumes = true
				}
			}
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
			out = append(out, TrustPrincipal{Type: t, Value: v})
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
