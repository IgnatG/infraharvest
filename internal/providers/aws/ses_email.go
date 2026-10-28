// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// SesLister lists the service "ses": the SES (v1) domain and email
// identities, the email templates, the configuration sets, and the receipt
// rule sets with their rules.
type SesLister struct {
	Service
}

// List lists, in this order, domain identities, email identities,
// templates, configuration sets, and receipt rule sets each followed by its
// rules. Any error fails the service.
func (g *SesLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *SesLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ses.NewFromConfig(config)
	if err := g.listIdentities(client, types.IdentityTypeDomain, "aws_ses_domain_identity"); err != nil {
		return err
	}
	if err := g.listIdentities(client, types.IdentityTypeEmailAddress, "aws_ses_email_identity"); err != nil {
		return err
	}
	if err := g.listTemplates(client); err != nil {
		return err
	}
	if err := g.listConfigurationSets(client); err != nil {
		return err
	}
	return g.listReceiptRuleSets(client)
}

// listIdentities records every identity of identityType, a domain or an
// email address, by itself, as a resource of resourceType.
func (g *SesLister) listIdentities(client *ses.Client, identityType types.IdentityType, resourceType string) error {
	p := ses.NewListIdentitiesPaginator(client, &ses.ListIdentitiesInput{IdentityType: identityType}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, identity := range page.Identities {
			g.Resources = append(g.Resources, harvest.RecordBare(identity, identity, resourceType, "aws"))
		}
	}
	return nil
}

// listTemplates records the email templates by name, across pages. The
// SDK has no paginator for it.
func (g *SesLister) listTemplates(client *ses.Client) error {
	return paginateByMarker(func(token *string) (*string, error) {
		out, err := client.ListTemplates(g.Context(), &ses.ListTemplatesInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, template := range out.TemplatesMetadata {
			name := aws.ToString(template.Name)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_ses_template", "aws"))
		}
		return out.NextToken, nil
	})
}

// listConfigurationSets records the configuration sets by name, across
// pages. The SDK has no paginator for it.
func (g *SesLister) listConfigurationSets(client *ses.Client) error {
	return paginateByMarker(func(token *string) (*string, error) {
		out, err := client.ListConfigurationSets(g.Context(), &ses.ListConfigurationSetsInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, set := range out.ConfigurationSets {
			name := aws.ToString(set.Name)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_ses_configuration_set", "aws"))
		}
		return out.NextToken, nil
	})
}

// listReceiptRuleSets records the receipt rule sets by name, across pages,
// each followed by its rules. The SDK has no paginator for it.
func (g *SesLister) listReceiptRuleSets(client *ses.Client) error {
	return paginateByMarker(func(token *string) (*string, error) {
		out, err := client.ListReceiptRuleSets(g.Context(), &ses.ListReceiptRuleSetsInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, ruleSet := range out.RuleSets {
			name := aws.ToString(ruleSet.Name)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_ses_receipt_rule_set", "aws"))
			// A call per rule set, for rules the import may not keep.
			if !g.Wants("aws_ses_receipt_rule") {
				continue
			}
			if err := g.listReceiptRules(client, name); err != nil {
				return nil, err
			}
		}
		return out.NextToken, nil
	})
}

// listReceiptRules records the rules of the rule set ruleSetName by
// Terraform's import ID, "<rule set>:<rule>", which they are named after
// too, with the rule's and the set's names as attributes.
func (g *SesLister) listReceiptRules(client *ses.Client, ruleSetName string) error {
	out, err := client.DescribeReceiptRuleSet(g.Context(), &ses.DescribeReceiptRuleSetInput{RuleSetName: aws.String(ruleSetName)})
	if err != nil {
		return err
	}
	for _, rule := range out.Rules {
		name := aws.ToString(rule.Name)
		id := ruleSetName + ":" + name
		g.Resources = append(g.Resources, harvest.Record(id, id, "aws_ses_receipt_rule", "aws", map[string]string{
			"name":          name,
			"rule_set_name": ruleSetName,
		}))
	}
	return nil
}
