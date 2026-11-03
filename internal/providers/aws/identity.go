// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// profileName matches a shared-configuration profile's name.
var profileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@+=,-]{0,127}$`)

// roleReference matches a role ARN, whose account is an account ID or
// {account}, or a role's name, which may have a path.
var roleReference = regexp.MustCompile(`^(arn:aws[a-z-]*:iam::([0-9]{12}|\{account\}):role/[\w+=,.@/-]+|[\w+=,.@][\w+=,.@/-]{0,511})$`)

// IdentityKeys are the keys of an environment's aws section, beside
// accounts, that name the identity its accounts are read with: profile,
// the shared-configuration profile whose credentials the import uses
// (as --profile does, SSO and MFA included), and assume_role, the role to
// assume in each account, as a role ARN or a role name in the account
// (as --assume-role does). Neither is a secret.
func IdentityKeys() []harvest.IdentityKey {
	return []harvest.IdentityKey{
		{Key: "profile", Match: profileName, Format: "a profile name"},
		{Key: "assume_role", Match: roleReference, Format: "a role name or ARN, whose account may be {account}", Check: checkRoleAccounts},
	}
}

// checkRoleAccounts returns why role, an ARN, can't be the role of the
// environment whose accounts are accounts: it is in an account other
// than the one the environment reads, or in one of several.
func checkRoleAccounts(role string, accounts []string) error {
	if !strings.HasPrefix(role, "arn:") || strings.Contains(role, "{account}") {
		return nil
	}
	fields := strings.SplitN(role, ":", 6)
	if len(fields) < 5 {
		return nil
	}
	if len(accounts) != 1 || accounts[0] != fields[4] {
		return fmt.Errorf("%s is in account %s, but the environment reads %s: use {account} in the ARN, or a role name", role, fields[4], strings.Join(accounts, ", "))
	}
	return nil
}

// RoleARN returns the role an environment's assume_role names in account:
// an ARN with {account} made account, an ARN as it is, or a role name in
// the account.
func RoleARN(role, account string) string {
	if strings.HasPrefix(role, "arn:") {
		return strings.ReplaceAll(role, "{account}", account)
	}
	return "arn:aws:iam::" + account + ":role/" + role
}
