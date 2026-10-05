// Copyright 2020 The Terraformer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package aws

import (
	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/IgnatG/infraharvest/terraformutils"
)

type EcrGenerator struct {
	AWSService
}

func (g *EcrGenerator) InitResources() error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}

	svc := ecr.NewFromConfig(config)

	p := ecr.NewDescribeRepositoriesPaginator(svc, &ecr.DescribeRepositoriesInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, e := p.NextPage(g.Context())
		if e != nil {
			return e
		}
		for _, repository := range page.Repositories {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				*repository.RepositoryName,
				*repository.RepositoryName,
				"aws_ecr_repository",
				"aws"))

			_, err := svc.GetRepositoryPolicy(g.Context(), &ecr.GetRepositoryPolicyInput{
				RepositoryName: repository.RepositoryName,
				RegistryId:     repository.RegistryId,
			})
			if err == nil {
				g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
					*repository.RepositoryName,
					*repository.RepositoryName,
					"aws_ecr_repository_policy",
					"aws"))
			}

			_, err = svc.GetLifecyclePolicy(g.Context(), &ecr.GetLifecyclePolicyInput{
				RepositoryName: repository.RepositoryName,
				RegistryId:     repository.RegistryId,
			})
			if err == nil {
				g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
					*repository.RepositoryName,
					*repository.RepositoryName,
					"aws_ecr_lifecycle_policy",
					"aws"))
			}
		}
	}
	return nil
}
