// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal || aws

package cmd

import (
	"context"
	"reflect"
	"testing"
)

func TestAWSAccounts(t *testing.T) {
	ctx := context.Background()
	accounts, err := awsAccounts(ctx, ImportOptions{Accounts: []string{"111122223333", "444455556666"}, AssumeRole: DefaultAssumeRole})
	if err != nil || !reflect.DeepEqual(accounts, []string{"111122223333", "444455556666"}) {
		t.Errorf("accounts: %v, %v", accounts, err)
	}
	if accounts, err := awsAccounts(ctx, ImportOptions{AssumeRole: DefaultAssumeRole}); err != nil || accounts != nil {
		t.Errorf("one account: %v, %v", accounts, err)
	}
	for name, options := range map[string]ImportOptions{
		"both":                   {Accounts: []string{"111122223333"}, Organization: true, AssumeRole: DefaultAssumeRole},
		"a role for one account": {Accounts: []string{"111122223333"}, AssumeRole: "arn:aws:iam::111122223333:role/x"},
	} {
		if _, err := awsAccounts(ctx, options); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
