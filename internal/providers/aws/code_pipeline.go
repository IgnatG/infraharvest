// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// CodePipelineLister lists the region's CodePipeline pipelines and
// webhooks.
type CodePipelineLister struct {
	Service
}

// List lists the pipelines, then the webhooks. Either list
// failing fails the service.
func (g *CodePipelineLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := codepipeline.NewFromConfig(config)
	if err := g.listPipelines(g.Context(), client); err != nil {
		return err
	}
	return g.listWebhooks(g.Context(), client)
}

// listPipelines adds each pipeline as an aws_codepipeline, imported by its
// name.
func (g *CodePipelineLister) listPipelines(ctx context.Context, client *codepipeline.Client) error {
	p := codepipeline.NewListPipelinesPaginator(client, &codepipeline.ListPipelinesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, pipeline := range page.Pipelines {
			name := aws.ToString(pipeline.Name)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_codepipeline", "aws"))
		}
	}
	return nil
}

// listWebhooks adds each webhook as an aws_codepipeline_webhook, imported
// by its ARN, named after the webhook's name, which the ARN ends with.
func (g *CodePipelineLister) listWebhooks(ctx context.Context, client *codepipeline.Client) error {
	p := codepipeline.NewListWebhooksPaginator(client, &codepipeline.ListWebhooksInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, webhook := range page.Webhooks {
			arn := aws.ToString(webhook.Arn)
			g.Resources = append(g.Resources, harvest.RecordBare(arn, cloudControlName(arn), "aws_codepipeline_webhook", "aws"))
		}
	}
	return nil
}
