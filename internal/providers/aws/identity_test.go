// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"
	"testing"
)

func TestRoleARN(t *testing.T) {
	t.Parallel()
	for role, want := range map[string]string{
		"readonly":                                    "arn:aws:iam::111111111111:role/readonly",
		"audit/readonly":                              "arn:aws:iam::111111111111:role/audit/readonly",
		"arn:aws:iam::{account}:role/readonly":        "arn:aws:iam::111111111111:role/readonly",
		"arn:aws-us-gov:iam::111111111111:role/other": "arn:aws-us-gov:iam::111111111111:role/other",
	} {
		if got := RoleARN(role, "111111111111"); got != want {
			t.Errorf("RoleARN(%q) = %q, want %q", role, got, want)
		}
	}
}

func TestIdentityKeys(t *testing.T) {
	t.Parallel()
	keys := IdentityKeys()
	role := keys[1]
	for value, ok := range map[string]bool{
		"readonly": true, "arn:aws:iam::{account}:role/readonly": true, "arn:aws:iam::123456789012:role/r": true,
		"arn:aws:s3:::bucket": false, "a b": false, "": false,
	} {
		if role.Match.MatchString(value) != ok {
			t.Errorf("assume_role %q: want %v", value, ok)
		}
	}
	if err := role.Check("arn:aws:iam::222222222222:role/r", []string{"111111111111"}); err == nil || !strings.Contains(err.Error(), "222222222222") {
		t.Errorf("a role in another account: %v", err)
	}
	if err := role.Check("arn:aws:iam::111111111111:role/r", []string{"111111111111", "222222222222"}); err == nil {
		t.Error("a fixed role for several accounts")
	}
	if err := role.Check("arn:aws:iam::111111111111:role/r", []string{"111111111111"}); err != nil {
		t.Error(err)
	}
	if err := role.Check("arn:aws:iam::{account}:role/r", []string{"111111111111", "222222222222"}); err != nil {
		t.Error(err)
	}
}
