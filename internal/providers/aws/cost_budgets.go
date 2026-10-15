// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
)

// BudgetsLister lists the AWS Budgets budgets of the account (service
// budgets). Budgets are global, so it runs once, in the global
// pseudo-region.
type BudgetsLister struct {
	Service
}

// List lists the budgets of the account the credentials belong
// to. Terraform imports a budget as <account>:<budget name>, so the
// account comes first, from STS.
func (g *BudgetsLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	account, err := g.callerAccount(config)
	if err != nil {
		return err
	}
	accountID := aws.ToString(account)
	client := budgets.NewFromConfig(config)

	pages := budgets.NewDescribeBudgetsPaginator(client, &budgets.DescribeBudgetsInput{AccountId: account}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, budget := range page.Budgets {
			name := aws.ToString(budget.BudgetName)
			g.Resources = append(g.Resources, harvest.RecordBare(accountID+":"+name, name, "aws_budgets_budget", "aws"))
		}
	}
	return nil
}
