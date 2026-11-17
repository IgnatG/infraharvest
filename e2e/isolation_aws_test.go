// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build e2e && (!minimal || aws)

package e2e

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// awsTestAccounts returns the accounts of the emulator the AWS tests
// create resources in, each test in accounts of its own, which share one
// emulator on a runner.
func awsTestAccounts() []string {
	accounts := []string{
		queryAccount, regionsAccount, optionsAccount, movesAccount, driftAccount,
		layoutAccount, moduleMovesAccount, curatedMovesAccount,
		driftMCPAccount, driftMCPRulesAccount, driftUntrustedAccount,
		driftBackendAccount, mcpImportAccount,
		"717171717171", "818181818181",
	}
	accounts = slices.AppendSeq(accounts, maps.Keys(parallelAccounts))
	accounts = slices.AppendSeq(accounts, maps.Values(environmentAccounts))
	accounts = slices.AppendSeq(accounts, maps.Values(mcpAccounts))
	return accounts
}

// checkNoOtherAccounts fails the test if a file or path under out names an
// account of another test, which an import signed in as own would only
// show if it read the other test's resources or credentials: the tests
// share an emulator and each must see only its own account.
func checkNoOtherAccounts(t *testing.T, out string, own ...string) {
	t.Helper()
	others := slices.DeleteFunc(slices.Compact(slices.Sorted(slices.Values(awsTestAccounts()))), func(account string) bool {
		return slices.Contains(own, account)
	})
	for name, content := range outputFiles(t, out) {
		for _, account := range others {
			if strings.Contains(name, account) || strings.Contains(content, account) {
				t.Errorf("%s names account %s, which another test uses; this test signs in as %v", name, account, own)
			}
		}
	}
}
