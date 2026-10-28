// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/appsync"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// AppSyncLister lists the region's AppSync GraphQL APIs.
type AppSyncLister struct {
	Service
}

// List records every GraphQL API, merged APIs included, by its
// API ID, which is what Terraform imports it by, named after the API.
func (g *AppSyncLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *AppSyncLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	pages := appsync.NewListGraphqlApisPaginator(appsync.NewFromConfig(config), &appsync.ListGraphqlApisInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, api := range page.GraphqlApis {
			g.Resources = append(g.Resources, harvest.RecordBare(
				aws.ToString(api.ApiId),
				aws.ToString(api.Name),
				"aws_appsync_graphql_api",
				"aws"))
		}
	}
	return nil
}
