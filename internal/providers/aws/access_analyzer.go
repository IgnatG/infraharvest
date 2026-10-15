// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/accessanalyzer"
)

// AccessAnalyzerLister lists the IAM Access Analyzer analyzers of a
// region (service accessanalyzer).
type AccessAnalyzerLister struct {
	Service
}

// List lists every analyzer, whatever its type or status, by
// name: Terraform imports an analyzer by its name. The analyzers' archive
// rules are not listed.
func (g *AccessAnalyzerLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := accessanalyzer.NewFromConfig(config)

	var resources []harvest.Resource
	pages := accessanalyzer.NewListAnalyzersPaginator(client, &accessanalyzer.ListAnalyzersInput{}, stopOnDuplicateToken)
	for pages.HasMorePages() {
		page, err := pages.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, analyzer := range page.Analyzers {
			name := aws.ToString(analyzer.Name)
			resources = append(resources, harvest.RecordBare(name, name, "aws_accessanalyzer_analyzer", "aws"))
		}
	}
	g.Resources = resources
	return nil
}
