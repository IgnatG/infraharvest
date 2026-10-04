// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
)

// OrganizationAccounts lists the active accounts of the organization the
// credentials of profile belong to, sorted. It needs
// organizations:ListAccounts, which the management account (or a
// delegated administrator) has.
func OrganizationAccounts(ctx context.Context, profile string) ([]string, error) {
	service := &AWSService{}
	service.SetArgs(map[string]interface{}{"region": "", "profile": profile, "skip_region_validation": true})
	service.SetContext(ctx)
	config, err := service.generateConfig()
	if err != nil {
		return nil, err
	}
	var accounts []string
	pages := organizations.NewListAccountsPaginator(organizations.NewFromConfig(config), &organizations.ListAccountsInput{})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, a := range page.Accounts {
			if a.Status == orgtypes.AccountStatusActive {
				accounts = append(accounts, aws.ToString(a.Id))
			}
		}
	}
	sort.Strings(accounts)
	return accounts, nil
}
