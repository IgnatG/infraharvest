// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"testing"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

const recordBudgetsSTSNS = "https://sts.amazonaws.com/doc/2011-06-15/"

// recordBudgetsCallerIdentity answers STS GetCallerIdentity for account
// 123456789012.
func recordBudgetsCallerIdentity(call apiCall) string {
	return recordQuery(call.Op, recordBudgetsSTSNS, `<Account>123456789012</Account>
		<Arn>arn:aws:iam::123456789012:user/auditor</Arn><UserId>AIDAEXAMPLE</UserId>`)
}

// TestBudgetsPaginates (pagination_test.go) pins the IDs across two pages.
// This records the rest: the budgets of the caller's account, named after
// the budget, with no attributes.
func TestRecordBudgetsListsBudgetsOfTheAccount(t *testing.T) {
	t.Parallel()
	ctx := useFakeAPI(t, func(call apiCall) string {
		switch call.Op {
		case "GetCallerIdentity":
			return recordBudgetsCallerIdentity(call)
		case "DescribeBudgets":
			if got := recordJSONField(call, "AccountId"); got != "123456789012" {
				t.Errorf("budgets of account %q, want 123456789012", got)
			}
			return `{"Budgets":[
				{"BudgetName":"monthly total","BudgetType":"COST","TimeUnit":"MONTHLY","BudgetLimit":{"Amount":"100.0","Unit":"USD"}},
				{"BudgetName":"ec2-hours","BudgetType":"USAGE","TimeUnit":"QUARTERLY"}]}`
		}
		return recordUnexpected(t, call)
	})
	g := &BudgetsLister{}

	resources, err := g.List(ctx, harvest.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, resources,
		recordedResource{"aws_budgets_budget", "123456789012:monthly total", "monthly total", nil},
		recordedResource{"aws_budgets_budget", "123456789012:ec2-hours", "ec2-hours", nil},
	)
}

// Neither the account nor the budgets can be left out.
func TestRecordBudgetsReturnsErrors(t *testing.T) {
	t.Parallel()
	t.Run("GetCallerIdentity", func(t *testing.T) {
		ctx := useFakeAPI(t, func(call apiCall) string {
			if call.Op == "GetCallerIdentity" {
				return recordQueryError(recordBudgetsSTSNS, "AccessDenied")
			}
			return recordUnexpected(t, call)
		})
		g := &BudgetsLister{}

		assertRecordedError(t, listError(ctx, g), "AccessDenied")
	})
	t.Run("DescribeBudgets", func(t *testing.T) {
		ctx := useFakeAPI(t, func(call apiCall) string {
			if call.Op == "GetCallerIdentity" {
				return recordBudgetsCallerIdentity(call)
			}
			return recordJSONError("AccessDeniedException")
		})
		g := &BudgetsLister{}

		assertRecordedError(t, listError(ctx, g), "AccessDeniedException")
	})
}
