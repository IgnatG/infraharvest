// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// OrganizationLister lists the organization of AWS Organizations, its
// accounts, organizational units and service control policies, with the
// policies' attachments (service organization). Organizations is global,
// so it runs once, in the global pseudo-region.
type OrganizationLister struct {
	Service
}

// List records the organization when the credentials are the
// management account's, walks the organization's tree from each root,
// then lists the service control policies, each followed by its
// attachments. Every list is read across pages, and every error is
// returned: a part of the tree left out would leave out all that is below
// it.
func (g *OrganizationLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *OrganizationLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := organizations.NewFromConfig(config)

	if err := g.addOrganization(config, client); err != nil {
		return err
	}

	roots := organizations.NewListRootsPaginator(client, &organizations.ListRootsInput{}, stopOnDuplicateToken)
	for roots.HasMorePages() {
		page, err := roots.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, root := range page.Roots {
			if err := g.walk(client, aws.ToString(root.Id)); err != nil {
				return err
			}
		}
	}

	input := &organizations.ListPoliciesInput{Filter: orgtypes.PolicyTypeServiceControlPolicy}
	policies := organizations.NewListPoliciesPaginator(client, input, stopOnDuplicateToken)
	for policies.HasMorePages() {
		page, err := policies.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, policy := range page.Policies {
			if err := g.addPolicy(client, policy); err != nil {
				return err
			}
		}
	}
	return nil
}

// addOrganization records the organization by its ID (o-...), which
// Terraform imports it by. Only the management account manages the
// organization itself, so a delegated administrator, which may list the
// rest, doesn't record it.
func (g *OrganizationLister) addOrganization(config aws.Config, client *organizations.Client) error {
	described, err := client.DescribeOrganization(g.Context(), &organizations.DescribeOrganizationInput{})
	if err != nil {
		return fmt.Errorf("describe the organization: %w", err)
	}
	organization := described.Organization
	if organization == nil {
		return nil
	}
	account, err := g.callerAccount(config)
	if err != nil {
		return err
	}
	if aws.ToString(account) != aws.ToString(organization.MasterAccountId) {
		return nil
	}
	id := aws.ToString(organization.Id)
	g.Resources = append(g.Resources, harvest.Record(id, id, "aws_organizations_organization", "aws",
		map[string]string{"id": id, "arn": aws.ToString(organization.Arn)}))
	return nil
}

// walk records the accounts directly under parent, then each
// organizational unit under it followed by what that unit holds. Each
// account is recorded once, as an aws_organizations_account by its ID.
func (g *OrganizationLister) walk(client *organizations.Client, parent string) error {
	accounts := organizations.NewListAccountsForParentPaginator(client,
		&organizations.ListAccountsForParentInput{ParentId: aws.String(parent)}, stopOnDuplicateToken)
	for accounts.HasMorePages() {
		page, err := accounts.NextPage(g.Context())
		if err != nil {
			return fmt.Errorf("accounts of %s: %w", parent, err)
		}
		for _, account := range page.Accounts {
			id := aws.ToString(account.Id)
			g.Resources = append(g.Resources, harvest.Record(id, aws.ToString(account.Name), "aws_organizations_account", "aws",
				map[string]string{"id": id, "arn": aws.ToString(account.Arn)}))
		}
	}

	units := organizations.NewListOrganizationalUnitsForParentPaginator(client,
		&organizations.ListOrganizationalUnitsForParentInput{ParentId: aws.String(parent)}, stopOnDuplicateToken)
	for units.HasMorePages() {
		page, err := units.NextPage(g.Context())
		if err != nil {
			return fmt.Errorf("organizational units of %s: %w", parent, err)
		}
		for _, unit := range page.OrganizationalUnits {
			id := aws.ToString(unit.Id)
			g.Resources = append(g.Resources, harvest.Record(id, aws.ToString(unit.Name), "aws_organizations_organizational_unit", "aws",
				map[string]string{"id": id, "arn": aws.ToString(unit.Arn)}))
			if err := g.walk(client, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// addPolicy records policy by its ID, followed by its attachments, as
// <target ID>:<policy ID>. Both record whether AWS manages the policy, as
// it does FullAWSAccess, for the catalogue's rules that leave those out
// of the default selection.
func (g *OrganizationLister) addPolicy(client *organizations.Client, policy orgtypes.PolicySummary) error {
	policyID, policyName := aws.ToString(policy.Id), aws.ToString(policy.Name)
	awsManaged := strconv.FormatBool(policy.AwsManaged)
	g.Resources = append(g.Resources, harvest.Record(policyID, policyName, "aws_organizations_policy", "aws",
		map[string]string{"id": policyID, "arn": aws.ToString(policy.Arn), "aws_managed": awsManaged}))

	targets := organizations.NewListTargetsForPolicyPaginator(client,
		&organizations.ListTargetsForPolicyInput{PolicyId: policy.Id}, stopOnDuplicateToken)
	for targets.HasMorePages() {
		page, err := targets.NextPage(g.Context())
		if err != nil {
			return fmt.Errorf("targets of policy %s: %w", policyName, err)
		}
		for _, target := range page.Targets {
			targetID := aws.ToString(target.TargetId)
			g.Resources = append(g.Resources, harvest.Record(
				targetID+":"+policyID, "pa-"+targetID+":"+policyName, "aws_organizations_policy_attachment", "aws",
				map[string]string{"policy_id": policyID, "target_id": targetID, "policy_aws_managed": awsManaged}))
		}
	}
	return nil
}
