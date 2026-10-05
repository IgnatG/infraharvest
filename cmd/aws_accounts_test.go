// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal || aws

package cmd

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/report"
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
	if _, err := awsAccounts(ctx, ImportOptions{Accounts: []string{"111122223333"}, Organization: true, AssumeRole: DefaultAssumeRole}); err == nil {
		t.Error("both: want an error")
	}
	for name, options := range map[string]ImportOptions{
		"a role for one account": {Accounts: []string{"111122223333"}, AssumeRole: "arn:aws:iam::111122223333:role/x"},
		// Checked before the organization's accounts are listed.
		"a role for one account of the organization": {Organization: true, AssumeRole: "arn:aws:iam::111122223333:role/x"},
	} {
		if _, err := awsAccounts(ctx, options); err == nil || !strings.Contains(err.Error(), "{account}") {
			t.Errorf("%s: want an error about {account}, got %v", name, err)
		}
	}
}

// An account that can't be imported is a failure of the run; the others
// are imported.
func TestImportAWSAccountsGoesOn(t *testing.T) {
	activeRun = newEngineRun()
	defer func() { activeRun = nil }()
	options := ImportOptions{AssumeRole: DefaultAssumeRole, PathOutput: t.TempDir()}
	var roles []string
	importAccount := func(o ImportOptions) error {
		roles = append(roles, o.RoleARN)
		if strings.Contains(o.RoleARN, "111122223333") {
			return errors.New("AccessDenied: can't assume the role")
		}
		return nil
	}

	err := importAWSAccounts(options, []string{"111122223333", "444455556666"}, importAccount)

	if err != nil {
		t.Fatalf("the run must go on: %v", err)
	}
	want := []string{"arn:aws:iam::111122223333:role/infraharvest-readonly", "arn:aws:iam::444455556666:role/infraharvest-readonly"}
	if !reflect.DeepEqual(roles, want) {
		t.Errorf("roles %v, want %v", roles, want)
	}
	if len(activeRun.failures) != 1 || !strings.Contains(activeRun.failures[0].Error(), "account 111122223333: AccessDenied") {
		t.Errorf("failures %v", activeRun.failures)
	}
	if code := ExitCode(activeRun.finish(nil)); code != report.ExitIncomplete {
		t.Errorf("exit code %d, want %d", code, report.ExitIncomplete)
	}

	// An interrupt stops the run.
	roles = nil
	err = importAWSAccounts(options, []string{"111122223333", "444455556666"}, func(ImportOptions) error { return context.Canceled })
	if !errors.Is(err, context.Canceled) || len(roles) != 0 {
		t.Errorf("interrupted: %v", err)
	}
}

// --resources '*' stands for every service: the global ones are imported
// once, not once per region.
func TestGroupAWSResources(t *testing.T) {
	global, eastOnly, regional := groupAWSResources(ImportOptions{Resources: []string{"*"}, Excludes: []string{"iam"}})

	all := slices.Concat(global, eastOnly, regional)
	if slices.Contains(all, "*") {
		t.Errorf("* not expanded: %v", regional)
	}
	if !slices.Contains(global, "route53") || !slices.Contains(global, "cloudfront") || slices.Contains(regional, "route53") {
		t.Errorf("global services: %v", global)
	}
	if !slices.Contains(eastOnly, "wafv2_cloudfront") || !slices.Contains(regional, "vpc") {
		t.Errorf("east-only %v, regional %v", eastOnly, regional)
	}
	if slices.Contains(all, "iam") {
		t.Error("iam not excluded")
	}
	if want := len(providerServices(newAWSProvider())) - 1; len(all) != want {
		t.Errorf("%d services, want %d", len(all), want)
	}

	// Named services are grouped as they are.
	global, _, regional = groupAWSResources(ImportOptions{Resources: []string{"vpc", "iam"}})
	if !slices.Equal(global, []string{"iam"}) || !slices.Equal(regional, []string{"vpc"}) {
		t.Errorf("global %v, regional %v", global, regional)
	}
}
