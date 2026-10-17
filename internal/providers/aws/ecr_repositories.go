// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"errors"
	"fmt"

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

// List lists every repository, each followed by its policies. Failing to
// list repositories fails the service. A policy that fails to read, other
// than for having none, doesn't stop the other repositories: the errors
// are returned together as a partial error (see harvest.Partial), so what
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
		for _, repository := range page.Repositories {
			errs = append(errs, g.addRepository(client, repository))
		}
	}
	return harvest.Partial(errors.Join(errs...))
}

// addRepository records repository by name, then its repository policy and
// its lifecycle policy, each by the repository's name, which is how
// Terraform imports them. The policies are read from the repository's own
// registry. Only ECR's answer that there is no such policy, or no longer
// such a repository, means there is none; any other error is returned.
func (g *EcrLister) addRepository(client *ecr.Client, repository types.Repository) error {
	name := aws.ToString(repository.RepositoryName)
	g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_ecr_repository", "aws"))

	var errs []error
	_, err := client.GetRepositoryPolicy(g.Context(), &ecr.GetRepositoryPolicyInput{
		RegistryId:     repository.RegistryId,
		RepositoryName: repository.RepositoryName,
	})
	var noPolicy *types.RepositoryPolicyNotFoundException
	switch {
	case err == nil:
		g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_ecr_repository_policy", "aws"))
	case !errors.As(err, &noPolicy) && !ecrRepositoryGone(err):
		errs = append(errs, fmt.Errorf("the repository policy of %s: %w", name, err))
	}
	_, err = client.GetLifecyclePolicy(g.Context(), &ecr.GetLifecyclePolicyInput{
		RegistryId:     repository.RegistryId,
		RepositoryName: repository.RepositoryName,
	})
	var noLifecycle *types.LifecyclePolicyNotFoundException
	switch {
	case err == nil:
		g.Resources = append(g.Resources, harvest.RecordBare(name, name, "aws_ecr_lifecycle_policy", "aws"))
	case !errors.As(err, &noLifecycle) && !ecrRepositoryGone(err):
		errs = append(errs, fmt.Errorf("the lifecycle policy of %s: %w", name, err))
	}
	return errors.Join(errs...)
}

// ecrRepositoryGone reports whether err says the repository no longer
// exists, as when it was deleted since it was listed, or that the endpoint
// doesn't offer the operation, as an emulator may not (see
// operationUnsupported): either way there is no policy to import.
func ecrRepositoryGone(err error) bool {
	var gone *types.RepositoryNotFoundException
	return errors.As(err, &gone) || operationUnsupported(err)
}
