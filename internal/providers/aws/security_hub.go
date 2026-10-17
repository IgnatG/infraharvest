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

	// Security Hub has no call that only says whether it is enabled; the
	// enabled standards tell, so they are read first, and once.
	subscriptions, err := g.enabledStandards(client)
	if err != nil {
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
	g.Resources = append(g.Resources, subscriptions...)
	return nil
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

// enabledStandards returns the enabled standards, across pages, as
// aws_securityhub_standards_subscription, by the subscription's ARN, which
// Terraform imports them by, with the standard's own ARN as standards_arn,
// named after the standard and its version (see standardName).
func (g *SecurityhubLister) enabledStandards(client *securityhub.Client) ([]harvest.Resource, error) {
	var subscriptions []harvest.Resource
	pages := securityhub.NewGetEnabledStandardsPaginator(client, &securityhub.GetEnabledStandardsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return nil, err
		}
		for _, subscription := range page.StandardsSubscriptions {
			arn := aws.ToString(subscription.StandardsSubscriptionArn)
			subscriptions = append(subscriptions, harvest.Record(arn, standardName(arn), "aws_securityhub_standards_subscription", "aws",
				map[string]string{"standards_arn": aws.ToString(subscription.StandardsArn)}))
		}
	}
	return subscriptions, nil
}

// standardName returns the standard and version a standards subscription's
// ARN, arn, names: aws-foundational-security-best-practices/v/1.0.0 for
// arn:aws:securityhub:<region>:<account>:subscription/aws-foundational-security-best-practices/v/1.0.0.
func standardName(arn string) string {
	if _, standard, ok := strings.Cut(arn, ":subscription/"); ok {
		return standard
	}
	return arn
}
