// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/service/kinesis"

	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// KinesisLister lists the region's Kinesis data streams (the `kinesis`
// service).
type KinesisLister struct {
	Service
}

// List records every stream as an aws_kinesis_stream, imported by
// its name, which it also records as the name attribute. Any listing error
// fails the service.
func (g *KinesisLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	pages := kinesis.NewListStreamsPaginator(kinesis.NewFromConfig(config), &kinesis.ListStreamsInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, name := range page.StreamNames {
			g.Resources = append(g.Resources, harvest.Record(name, name, "aws_kinesis_stream", "aws",
				map[string]string{"name": name}))
		}
	}
	return nil
}
