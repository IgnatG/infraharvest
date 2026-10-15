// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"

	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/securityhub"
)

// SecurityhubLister lists the Security Hub account, its member
// accounts and its standards subscriptions in a region (service
// securityhub).
type SecurityhubLister struct {
	Service
}

// securityHubNotEnabled is in the error Security Hub answers an account
// that hasn't enabled it.
const securityHubNotEnabled = "not subscribed to AWS Security Hub"

// List records the account, by its ID from STS, when it has
// enabled Security Hub in the region, then its members and its standards
// subscriptions. An account that hasn't enabled Security Hub has nothing
// to list, which is not an error.
func (g *SecurityhubLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	account, err := g.callerAccount(config)
	if err != nil {
		return err
	}
	client := securityhub.NewFromConfig(config)

	// Security Hub has no call that only says whether it is enabled; one
	// page of enabled standards tells.
	if _, err := client.GetEnabledStandards(g.Context(), &securityhub.GetEnabledStandardsInput{}); err != nil {
		if strings.Contains(err.Error(), securityHubNotEnabled) {
			return nil
		}
		return err
	}
	accountID := aws.ToString(account)
	g.Resources = append(g.Resources, harvest.RecordBare(accountID, accountID, "aws_securityhub_account", "aws"))

	if err := g.listMembers(client); err != nil {
		return err
	}
	return g.listStandardsSubscriptions(client)
}

// listMembers records the member accounts by account ID, with their email
// address when Security Hub has one.
func (g *SecurityhubLister) listMembers(client *securityhub.Client) error {
	pages := securityhub.NewListMembersPaginator(client, &securityhub.ListMembersInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, member := range page.Members {
			id := aws.ToString(member.AccountId)
			attributes := map[string]string{"account_id": id}
			if member.Email != nil {
				attributes["email"] = *member.Email
			}
			g.Resources = append(g.Resources, harvest.Record(id, "securityhub_member_"+id, "aws_securityhub_member", "aws", attributes))
		}
	}
	return nil
}

// listStandardsSubscriptions records the enabled standards by the
// subscription's ARN, which Terraform imports them by, with the
// standard's own ARN as standards_arn.
func (g *SecurityhubLister) listStandardsSubscriptions(client *securityhub.Client) error {
	pages := securityhub.NewGetEnabledStandardsPaginator(client, &securityhub.GetEnabledStandardsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, subscription := range page.StandardsSubscriptions {
			arn := aws.ToString(subscription.StandardsSubscriptionArn)
			g.Resources = append(g.Resources, harvest.Record(arn, arn, "aws_securityhub_standards_subscription", "aws",
				map[string]string{"standards_arn": aws.ToString(subscription.StandardsArn)}))
		}
	}
	return nil
}
