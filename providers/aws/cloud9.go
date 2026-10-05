// Copyright 2019 The Terraformer Authors.
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
	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/aws/aws-sdk-go-v2/service/cloud9"
	"github.com/aws/aws-sdk-go-v2/service/cloud9/types"
)

type Cloud9Generator struct {
	AWSService
}

func (g *Cloud9Generator) InitResources() error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}
	svc := cloud9.NewFromConfig(config)
	p := cloud9.NewListEnvironmentsPaginator(svc, &cloud9.ListEnvironmentsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, environmentID := range page.EnvironmentIds {
			details, err := svc.DescribeEnvironmentStatus(g.Context(), &cloud9.DescribeEnvironmentStatusInput{
				EnvironmentId: &environmentID,
			})
			if err != nil {
				return err
			}
			if details.Status == types.EnvironmentStatusError ||
				details.Status == types.EnvironmentStatusDeleting {
				continue
			}
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				environmentID,
				environmentID,
				"aws_cloud9_environment_ec2",
				"aws"))
		}
	}
	return nil
}
