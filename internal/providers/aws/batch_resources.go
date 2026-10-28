// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/batch"
	"github.com/aws/aws-sdk-go-v2/service/batch/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// BatchLister lists the region's AWS Batch compute environments, job
// definitions and job queues.
type BatchLister struct {
	Service
}

// List records the compute environments, then the active job
// definitions, then the job queues, each by the ID Terraform imports it by. The first error stops the listing and
// is returned.
func (g *BatchLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *BatchLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := batch.NewFromConfig(config)
	for _, list := range []func(*batch.Client) error{
		g.listBatchComputeEnvironments,
		g.listBatchJobDefinitions,
		g.listBatchJobQueues,
	} {
		if err := list(client); err != nil {
			return err
		}
	}
	return nil
}

// listBatchComputeEnvironments records each compute environment by name,
// which is what Terraform imports it by, but those being deleted or
// deleted, which Terraform reads as gone.
func (g *BatchLister) listBatchComputeEnvironments(client *batch.Client) error {
	pages := batch.NewDescribeComputeEnvironmentsPaginator(client, &batch.DescribeComputeEnvironmentsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, environment := range page.ComputeEnvironments {
			switch environment.Status {
			case types.CEStatusDeleting, types.CEStatusDeleted:
				continue
			}
			name := aws.ToString(environment.ComputeEnvironmentName)
			g.Resources = append(g.Resources, harvest.Record(
				name,
				name,
				"aws_batch_compute_environment",
				"aws",
				map[string]string{"compute_environment_name": name}))
		}
	}
	return nil
}

// listBatchJobDefinitions records every active revision of every job
// definition as a resource of its own, by its ARN, which ends in the
// revision and is what Terraform imports it by, named <name>:<revision>;
// inactive (deregistered) revisions are left out. The ARN is kept as an
// attribute too.
func (g *BatchLister) listBatchJobDefinitions(client *batch.Client) error {
	input := &batch.DescribeJobDefinitionsInput{Status: aws.String("ACTIVE")}
	pages := batch.NewDescribeJobDefinitionsPaginator(client, input, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, definition := range page.JobDefinitions {
			arn := aws.ToString(definition.JobDefinitionArn)
			name := fmt.Sprintf("%s:%d", aws.ToString(definition.JobDefinitionName), aws.ToInt32(definition.Revision))
			g.Resources = append(g.Resources, harvest.Record(
				arn,
				name,
				"aws_batch_job_definition",
				"aws",
				map[string]string{"arn": arn}))
		}
	}
	return nil
}

// listBatchJobQueues records each job queue by its ARN, which is what
// Terraform imports it by, named after its name, but those being deleted
// or deleted.
func (g *BatchLister) listBatchJobQueues(client *batch.Client) error {
	pages := batch.NewDescribeJobQueuesPaginator(client, &batch.DescribeJobQueuesInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, queue := range page.JobQueues {
			switch queue.Status {
			case types.JQStatusDeleting, types.JQStatusDeleted:
				continue
			}
			g.Resources = append(g.Resources, harvest.Record(
				aws.ToString(queue.JobQueueArn),
				aws.ToString(queue.JobQueueName),
				"aws_batch_job_queue",
				"aws",
				map[string]string{}))
		}
	}
	return nil
}
