// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/devicefarm"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// DeviceFarmLister lists the Device Farm projects. Device Farm runs only
// in us-west-2, and the lister does not check the region: listing it
// anywhere else is left to fail as the endpoint does.
type DeviceFarmLister struct {
	Service
}

// List lists every project as an aws_devicefarm_project, imported
// by its ARN and named after its name.
func (g *DeviceFarmLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *DeviceFarmLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := devicefarm.NewFromConfig(config)
	p := devicefarm.NewListProjectsPaginator(client, &devicefarm.ListProjectsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, project := range page.Projects {
			g.Resources = append(g.Resources, harvest.RecordBare(
				aws.ToString(project.Arn), aws.ToString(project.Name), "aws_devicefarm_project", "aws"))
		}
	}
	return nil
}
