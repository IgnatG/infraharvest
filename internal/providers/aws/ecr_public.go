// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecrpublic"
	"github.com/infraharvest/infraharvest-cli/internal/harvest"
)

// EcrPublicLister lists the global service "ecrpublic": the public ECR
// repositories of the account.
type EcrPublicLister struct {
	Service
}

// List records every public repository by name. ECR Public has
// its API in us-east-1 only, so the client is pointed there whatever
// region the service is listed for. Any error fails the service.
func (g *EcrPublicLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ecrpublic.NewFromConfig(config, func(o *ecrpublic.Options) {
		o.Region = GlobalHomeRegion
	})
	p := ecrpublic.NewDescribeRepositoriesPaginator(client, &ecrpublic.DescribeRepositoriesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, repository := range page.Repositories {
			name := aws.ToString(repository.RepositoryName)
			g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_ecrpublic_repository", "aws"))
		}
	}
	return nil
}
