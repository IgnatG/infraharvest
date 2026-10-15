// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codecommit"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// CodeCommitLister lists the region's CodeCommit repositories and
// approval rule templates.
type CodeCommitLister struct {
	Service
}

// List lists the repositories, then the approval rule templates,
// both by name. Either list failing fails the service.
func (g *CodeCommitLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := codecommit.NewFromConfig(config)
	if err := g.listRepositories(g.Context(), client); err != nil {
		return err
	}
	return g.listApprovalRuleTemplates(g.Context(), client)
}

// listRepositories adds each repository as an aws_codecommit_repository,
// imported by its name.
func (g *CodeCommitLister) listRepositories(ctx context.Context, client *codecommit.Client) error {
	p := codecommit.NewListRepositoriesPaginator(client, &codecommit.ListRepositoriesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, repo := range page.Repositories {
			name := aws.ToString(repo.RepositoryName)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_codecommit_repository", "aws"))
		}
	}
	return nil
}

// listApprovalRuleTemplates adds each approval rule template as an
// aws_codecommit_approval_rule_template, imported by its name.
func (g *CodeCommitLister) listApprovalRuleTemplates(ctx context.Context, client *codecommit.Client) error {
	p := codecommit.NewListApprovalRuleTemplatesPaginator(client, &codecommit.ListApprovalRuleTemplatesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, name := range page.ApprovalRuleTemplateNames {
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_codecommit_approval_rule_template", "aws"))
		}
	}
	return nil
}
