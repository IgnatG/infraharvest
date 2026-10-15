// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"github.com/IgnatG/infraharvest/internal/harvest"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
)

// EcrLister lists the service "ecr": the private ECR repositories, each
// with its repository policy and its lifecycle policy when it has them.
type EcrLister struct {
	Service
}

// List lists every repository, each followed by its policies.
// Failing to list repositories fails the service.
func (g *EcrLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ecr.NewFromConfig(config)
	p := ecr.NewDescribeRepositoriesPaginator(client, &ecr.DescribeRepositoriesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, repository := range page.Repositories {
			g.addRepository(client, repository)
		}
	}
	return nil
}

// addRepository records repository by name, then its repository policy and
// its lifecycle policy, each by the repository's name, which is how
// Terraform imports them. The policies are read from the repository's own
// registry. Reading a policy fails when the repository has none, so any
// error is taken to mean there is no policy.
func (g *EcrLister) addRepository(client *ecr.Client, repository types.Repository) {
	name := aws.ToString(repository.RepositoryName)
	g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_ecr_repository", "aws"))

	if _, err := client.GetRepositoryPolicy(g.Context(), &ecr.GetRepositoryPolicyInput{
		RegistryId:     repository.RegistryId,
		RepositoryName: repository.RepositoryName,
	}); err == nil {
		g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_ecr_repository_policy", "aws"))
	}
	if _, err := client.GetLifecyclePolicy(g.Context(), &ecr.GetLifecyclePolicyInput{
		RegistryId:     repository.RegistryId,
		RepositoryName: repository.RepositoryName,
	}); err == nil {
		g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_ecr_lifecycle_policy", "aws"))
	}
}
