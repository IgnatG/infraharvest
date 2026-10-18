// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/service/codebuild"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// CodeBuildLister lists the region's CodeBuild projects.
type CodeBuildLister struct {
	Service
}

// List lists every project as an aws_codebuild_project. The list
// returns only names, which are also the Terraform import IDs.
func (g *CodeBuildLister) List() error {
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
