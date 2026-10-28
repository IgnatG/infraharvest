// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"errors"
	"fmt"

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
// other repositories: the errors are returned together as a partial error (see harvest.Partial), so what
// was listed is imported and the listing reads as incomplete rather than
// as repositories without policies.
func (g *EcrLister) List() error {
	config, err := g.sdkConfig()
	if err != nil {
		return err
	}
	client := ecr.NewFromConfig(config)
	var errs []error
	p := ecr.NewDescribeRepositoriesPaginator(client, &ecr.DescribeRepositoriesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		parts := make([][]harvest.Resource, len(page.Repositories))
		partErrs := make([]error, len(page.Repositories))
		inParallel(len(page.Repositories), resourceParallel, func(i int) {
			parts[i], partErrs[i] = g.repository(client, page.Repositories[i])
		})
		for i := range page.Repositories {
			g.Resources = append(g.Resources, parts[i]...)
			errs = append(errs, partErrs[i])
		}
	}
	return harvest.Partial(errors.Join(errs...))
}

// repository returns repository, by name, then its repository policy and
// its lifecycle policy, each by the repository's name, which is how
// Terraform imports them. The policies are read from the repository's own
// registry, a call each, unless the import doesn't keep their type. Only
// ECR's answer that there is no such policy, or no longer such a
// repository, means there is none; any other error is returned, with what
// was read.
func (g *EcrLister) repository(client *ecr.Client, repository types.Repository) ([]harvest.Resource, error) {
	name := aws.ToString(repository.RepositoryName)
	resources := []harvest.Resource{harvest.RecordBare(name, name, "aws_ecr_repository", "aws")}

	var errs []error
	if g.wants("aws_ecr_repository_policy") {
		_, err := client.GetRepositoryPolicy(g.Context(), &ecr.GetRepositoryPolicyInput{
			RegistryId:     repository.RegistryId,
			RepositoryName: repository.RepositoryName,
		})
		var noPolicy *types.RepositoryPolicyNotFoundException
		switch {
		case err == nil:
			resources = append(resources, harvest.RecordBare(name, name, "aws_ecr_repository_policy", "aws"))
		case !errors.As(err, &noPolicy) && !ecrRepositoryGone(err):
			errs = append(errs, fmt.Errorf("the repository policy of %s: %w", name, err))
		}
	}
	if g.wants("aws_ecr_lifecycle_policy") {
		_, err := client.GetLifecyclePolicy(g.Context(), &ecr.GetLifecyclePolicyInput{
			RegistryId:     repository.RegistryId,
			RepositoryName: repository.RepositoryName,
		})
		var noLifecycle *types.LifecyclePolicyNotFoundException
		switch {
		case err == nil:
			resources = append(resources, harvest.RecordBare(name, name, "aws_ecr_lifecycle_policy", "aws"))
		case !errors.As(err, &noLifecycle) && !ecrRepositoryGone(err):
			errs = append(errs, fmt.Errorf("the lifecycle policy of %s: %w", name, err))
		}
	}
	return resources, errors.Join(errs...)
}

// ecrRepositoryGone reports whether err says the repository no longer
// exists, as when it was deleted since it was listed, or that the endpoint
// doesn't offer the operation, as an emulator may not (see
// operationUnsupported): either way there is no policy to import.
func ecrRepositoryGone(err error) bool {
	var gone *types.RepositoryNotFoundException
	return errors.As(err, &gone) || operationUnsupported(err)
}
