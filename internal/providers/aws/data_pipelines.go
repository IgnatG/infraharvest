// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/datapipeline"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// DataPipelineLister lists a region's Data Pipeline pipelines by ID,
// which is what Terraform imports them with, each named by its name as AWS
// gives it. Names need not be unique, so two pipelines may share one;
// their IDs tell them apart. Data Pipeline is closed to new customers,
// but existing pipelines can still be imported.
type DataPipelineLister struct {
	Service
}

// List lists the pipelines page by page and returns the first
// error.
func (g *DataPipelineLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	svc := datapipeline.NewFromConfig(config)
	p := datapipeline.NewListPipelinesPaginator(svc, &datapipeline.ListPipelinesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, pipeline := range page.PipelineIdList {
			g.Resources = append(g.Resources, harvest.RecordBare(aws.ToString(pipeline.Id), aws.ToString(pipeline.Name), "aws_datapipeline_pipeline", "aws"))
		}
	}
	return nil
}
