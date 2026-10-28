// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/codebuild"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// CodeBuildLister lists the region's CodeBuild projects.
type CodeBuildLister struct {
	Service
}

// List lists every project as an aws_codebuild_project. The list
// returns only names, which are also the Terraform import IDs.
func (g *CodeBuildLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *CodeBuildLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := codebuild.NewFromConfig(config)
	p := codebuild.NewListProjectsPaginator(client, &codebuild.ListProjectsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, name := range page.Projects {
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_codebuild_project", "aws"))
		}
	}
	return nil
}
