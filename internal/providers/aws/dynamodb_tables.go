// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// DynamoDbLister lists the region's DynamoDB tables (the `dynamodb`
// service).
type DynamoDbLister struct {
	Service
}

// List records every table as an aws_dynamodb_table, imported by
// its name. Any listing error fails the service.
func (g *DynamoDbLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	pages := dynamodb.NewListTablesPaginator(dynamodb.NewFromConfig(config), &dynamodb.ListTablesInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, name := range page.TableNames {
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_dynamodb_table", "aws"))
		}
	}
	return nil
}
