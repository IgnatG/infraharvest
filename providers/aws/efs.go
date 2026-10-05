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
	"fmt"

	"github.com/IgnatG/infraharvest/terraformutils"

	"github.com/aws/aws-sdk-go-v2/service/efs"
)

type EfsGenerator struct {
	AWSService
}

func (g *EfsGenerator) InitResources() error {
	config, e := g.generateConfig()
	if e != nil {
		return e
	}
	svc := efs.NewFromConfig(config)
	if err := g.loadFileSystem(svc); err != nil {
		return err
	}
	if err := g.loadAccessPoint(svc); err != nil {
		return err
	}
	return nil
}

func (g *EfsGenerator) loadFileSystem(svc *efs.Client) error {
	p := efs.NewDescribeFileSystemsPaginator(svc, &efs.DescribeFileSystemsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, fileSystem := range page.FileSystems {
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				StringValue(fileSystem.FileSystemId),
				StringValue(fileSystem.FileSystemId),
				"aws_efs_file_system",
				"aws"))

			targetsResponse, err := svc.DescribeMountTargets(g.Context(), &efs.DescribeMountTargetsInput{
				FileSystemId: fileSystem.FileSystemId,
			})
			if err != nil {
				fmt.Println(err.Error())
				continue
			}
			for _, mountTarget := range targetsResponse.MountTargets {
				g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
					StringValue(mountTarget.MountTargetId),
					StringValue(mountTarget.MountTargetId),
					"aws_efs_mount_target",
					"aws"))
			}

			policyResponse, err := svc.DescribeFileSystemPolicy(g.Context(), &efs.DescribeFileSystemPolicyInput{
				FileSystemId: fileSystem.FileSystemId,
			})
			if err != nil {
				fmt.Println(err.Error())
				continue
			}
			g.Resources = append(g.Resources, terraformutils.NewResource(
				StringValue(fileSystem.FileSystemId),
				StringValue(fileSystem.FileSystemId),
				"aws_efs_file_system_policy",
				"aws",
				map[string]string{
					"file_system_id": StringValue(fileSystem.FileSystemId),
					"policy":         StringValue(policyResponse.Policy),
				}))
		}
	}
	return nil
}

func (g *EfsGenerator) loadAccessPoint(svc *efs.Client) error {
	p := efs.NewDescribeAccessPointsPaginator(svc, &efs.DescribeAccessPointsInput{}, stopOnDuplicateToken)
	for p.HasMorePages() {
		page, err := p.NextPage(g.Context())
		if err != nil {
			return err
		}
		for _, fileSystem := range page.AccessPoints {
			id := StringValue(fileSystem.AccessPointId)
			g.Resources = append(g.Resources, terraformutils.NewSimpleResource(
				id,
				id,
				"aws_efs_access_point",
				"aws"))
		}
	}
	return nil
}
