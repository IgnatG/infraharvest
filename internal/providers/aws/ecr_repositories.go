// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/IgnatG/infraharvest/internal/harvest"
)

// EcrLister lists the service "ecr": the private ECR repositories, each
// with its repository policy and its lifecycle policy when it has them.
type EcrLister struct {
	Service
}

// List lists every repository, each followed by its policies: the
// repositories of a page are read resourceParallel at a time, and recorded
// in the page's order. Failing to list repositories fails the service. A
// policy that fails to read, other than for having none, doesn't stop the
// other repositories: the errors are returned together as a partial error
// (see harvest.Parts), so what was listed is imported and the listing
// reads as incomplete rather than as repositories without policies.
func (g *EcrLister) List(ctx context.Context, s harvest.Settings) ([]harvest.Resource, error) {
	return g.Run(ctx, s, g.list)
}

func (g *EcrLister) list() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ecr.NewFromConfig(config)
	var parts harvest.Parts
	p := ecr.NewDescribeRepositoriesPaginator(client, &ecr.DescribeRepositoriesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return parts.Fail(err)
		}
		found := make([][]harvest.Resource, len(page.Repositories))
		repositoryParts := make([]harvest.Parts, len(page.Repositories))
		inParallel(len(page.Repositories), resourceParallel, func(i int) {
			found[i] = g.repository(client, page.Repositories[i], &repositoryParts[i])
		})
		for i := range page.Repositories {
			g.Resources = append(g.Resources, found[i]...)
			parts.Merge(&repositoryParts[i])
		}
	}
	return parts.Err()
}

// repository returns repository, by name, then its repository policy and
// its lifecycle policy, each by the repository's name, which is how
// Terraform imports them. The policies are read from the repository's own
// registry, a call each, unless the import doesn't keep their type. An
// answer that leaves nothing out (see awsCodes), as ECR's that there is no
// such policy, or no longer such a repository, means there is none; any
// other failure is added to parts, and what was read is returned.
func (g *EcrLister) repository(client *ecr.Client, repository types.Repository, parts *harvest.Parts) []harvest.Resource {
	name := aws.ToString(repository.RepositoryName)
	resources := []harvest.Resource{harvest.RecordBare(name, name, "aws_ecr_repository", "aws")}

	if g.Wants("aws_ecr_repository_policy") {
		_, err := client.GetRepositoryPolicy(g.Context(), &ecr.GetRepositoryPolicyInput{
			RegistryId:     repository.RegistryId,
			RepositoryName: repository.RepositoryName,
		})
		if err == nil {
			resources = append(resources, harvest.RecordBare(name, name, "aws_ecr_repository_policy", "aws"))
		}
		parts.Add(awsCodes, "the repository policy of "+name, err)
	}
	if g.Wants("aws_ecr_lifecycle_policy") {
		_, err := client.GetLifecyclePolicy(g.Context(), &ecr.GetLifecyclePolicyInput{
			RegistryId:     repository.RegistryId,
			RepositoryName: repository.RepositoryName,
		})
		if err == nil {
			resources = append(resources, harvest.RecordBare(name, name, "aws_ecr_lifecycle_policy", "aws"))
		}
		parts.Add(awsCodes, "the lifecycle policy of "+name, err)
	}
	return resources
}
